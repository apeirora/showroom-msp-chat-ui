// Package accesspolicy binds browser access to operator-reviewed source identity.
package accesspolicy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

const OAuthImage = "quay.io/oauth2-proxy/oauth2-proxy@sha256:8498b0d0ef0a7b29686414000a08aee467f02d0299c9ed1e006a8f33fc017916"

var ErrUnmapped = errors.New("instance has no operator-reviewed access mapping")

type Source struct {
	Cluster   string `json:"cluster"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
}

type Entry struct {
	UID              string `json:"uid"`
	Namespace        string `json:"namespace"`
	Name             string `json:"name"`
	Source           Source `json:"source"`
	Issuer           string `json:"issuer"`
	ClientID         string `json:"clientID"`
	ClientSecretName string `json:"clientSecretName"`
}

type Policy struct {
	KCPServer            string  `json:"kcpServer"`
	KCPCA                string  `json:"kcpCA,omitempty"`
	CredentialsNamespace string  `json:"credentialsNamespace"`
	ProxyImage           string  `json:"proxyImage"`
	Entries              []Entry `json:"entries"`
}

// Decode strictly reads one bounded document; credentials are never logged.
func Decode(reader io.Reader, target any) error {
	data, err := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return errors.New("access configuration cannot be read or exceeds 1 MiB")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return errors.New("invalid access configuration JSON")
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("access configuration requires one JSON document")
	}
	return nil
}

func HTTPSURL(raw string, originOnly bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || strings.ContainsAny(raw, "\r\n\t ") {
		return errors.New("access endpoints must be explicit HTTPS URLs without credentials or query")
	}
	if originOnly && u.Path != "" {
		return errors.New("KCP server must be an HTTPS origin without a path")
	}
	return nil
}

func (s Source) Validate() error {
	if len(validation.IsDNS1123Label(s.Cluster)) != 0 || len(validation.IsDNS1123Label(s.Namespace)) != 0 ||
		len(validation.IsDNS1123Subdomain(s.Name)) != 0 || s.UID == "" || strings.TrimSpace(s.UID) != s.UID {
		return errors.New("access mapping requires exact source cluster, namespace, name and UID")
	}
	return nil
}

func Parse(reader io.Reader) (*Policy, error) {
	var p Policy
	if err := Decode(reader, &p); err != nil {
		return nil, err
	}
	if err := HTTPSURL(p.KCPServer, true); err != nil {
		return nil, err
	}
	if len(validation.IsDNS1123Label(p.CredentialsNamespace)) != 0 ||
		!regexp.MustCompile(`^[a-z0-9][a-z0-9./:_-]*@sha256:[a-f0-9]{64}$`).MatchString(p.ProxyImage) {
		return nil, errors.New("access policy requires a credential namespace and digest-pinned proxy image")
	}
	uids, local, sources := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, e := range p.Entries {
		if e.UID == "" || len(validation.IsDNS1123Label(e.Namespace)) != 0 || len(validation.IsDNS1123Subdomain(e.Name)) != 0 ||
			e.ClientID == "" || strings.ContainsAny(e.ClientID, "\r\n\t ") || len(validation.IsDNS1123Subdomain(e.ClientSecretName)) != 0 {
			return nil, errors.New("invalid access mapping identity or client reference")
		}
		if err := e.Source.Validate(); err != nil {
			return nil, err
		}
		if err := HTTPSURL(e.Issuer, false); err != nil {
			return nil, err
		}
		key, source := e.Namespace+"/"+e.Name, e.Source.Cluster+"/"+e.Source.Namespace+"/"+e.Source.Name
		if uids[e.UID] || local[key] || sources[source] {
			return nil, errors.New("duplicate access mapping")
		}
		uids[e.UID], local[key], sources[source] = true, true, true
	}
	return &p, nil
}

func (p *Policy) Select(obj metav1.Object) (Entry, error) {
	if p != nil && obj.GetUID() != "" {
		for _, e := range p.Entries {
			if e.UID == string(obj.GetUID()) && e.Namespace == obj.GetNamespace() && e.Name == obj.GetName() {
				return e, nil
			}
		}
	}
	return Entry{}, ErrUnmapped
}

func (s Source) Path() string {
	return fmt.Sprintf("/clusters/%s/apis/ui.privatellms.msp/v1alpha1/namespaces/%s/chatuiinstances/%s", s.Cluster, s.Namespace, s.Name)
}
