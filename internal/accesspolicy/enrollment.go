package accesspolicy

import (
	"crypto/sha3"
	"encoding/hex"
	"errors"
	"io"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

const EnrollmentDataKey = "enrollment.json"

// Workspace is platform configuration, never inferred from instance metadata.
// Permission to use each enrolled instance still comes from its exact KCP GET.
type Workspace struct {
	Cluster          string `json:"cluster"`
	Issuer           string `json:"issuer"`
	ClientID         string `json:"clientID"`
	ClientSecretName string `json:"clientSecretName"`
}

// Enrollment names one platform-owned ConfigMap in CredentialsNamespace.
// Its producer is separate from the tenant-facing ChatUIInstance API.
type Enrollment struct {
	ConfigMapName string      `json:"configMapName"`
	Workspaces    []Workspace `json:"workspaces"`
}

// EnrollmentDocument retains source pins while the corresponding MSP UID exists,
// including while its source is absent. This prevents rebinding surviving state
// to a newly created source object with the same name.
type EnrollmentDocument struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
	Pins    []Entry `json:"pins"`
}

func (e *Enrollment) Validate() error {
	if e == nil {
		return nil
	}
	if len(validation.IsDNS1123Subdomain(e.ConfigMapName)) != 0 || len(e.Workspaces) == 0 {
		return errors.New("enrollment requires a named ConfigMap and explicit workspaces")
	}
	seen := map[string]bool{}
	for _, w := range e.Workspaces {
		if len(validation.IsDNS1123Label(w.Cluster)) != 0 || seen[w.Cluster] ||
			w.ClientID == "" || strings.ContainsAny(w.ClientID, "\r\n\t ") ||
			len(validation.IsDNS1123Subdomain(w.ClientSecretName)) != 0 || HTTPSURL(w.Issuer, false) != nil {
			return errors.New("invalid or duplicate enrollment workspace")
		}
		seen[w.Cluster] = true
	}
	return nil
}

// RuntimeNamespace is the pinned Showroom api-syncagent v0.7 naming template.
// It must not be selected by source annotations or tenant-supplied templates.
func RuntimeNamespace(cluster, namespace string) string {
	prefix := cluster + "-" + namespace
	if len(prefix) > 36 {
		prefix = prefix[:36]
	}
	prefix = strings.Trim(prefix, "-.")
	digest := sha3.Sum256([]byte(cluster + "/" + namespace))
	return "chat-" + prefix + "-" + hex.EncodeToString(digest[:])[:20]
}

func (e *Enrollment) Allows(entry Entry) bool {
	if e == nil || entry.Name != entry.Source.Name ||
		entry.Namespace != RuntimeNamespace(entry.Source.Cluster, entry.Source.Namespace) {
		return false
	}
	for _, w := range e.Workspaces {
		if w.Cluster == entry.Source.Cluster && w.Issuer == entry.Issuer &&
			w.ClientID == entry.ClientID && w.ClientSecretName == entry.ClientSecretName {
			return true
		}
	}
	return false
}

// DecodeEnrollment validates the document and its durable identity pins. Pins may
// outlive a workspace's approval or client configuration. Current authorization
// is checked by SelectEnrolled for the selected entry, so policy revocation does
// not invalidate unrelated entries or prevent the producer repairing the map.
func (p *Policy) DecodeEnrollment(reader io.Reader) (*EnrollmentDocument, error) {
	if p == nil || p.Enrollment == nil {
		return nil, ErrUnmapped
	}
	if err := p.Enrollment.Validate(); err != nil {
		return nil, err
	}
	var doc EnrollmentDocument
	if err := Decode(reader, &doc); err != nil {
		return nil, err
	}
	if doc.Version != 1 || len(doc.Entries) > 1000 || len(doc.Pins) > 1000 {
		return nil, errors.New("invalid enrollment version or size")
	}
	// Reuse the same identity and duplicate checks as reviewed static mappings.
	for _, list := range [][]Entry{doc.Entries, doc.Pins} {
		uids, names, sources, sourceUIDs := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
		for _, item := range list {
			key := item.Namespace + "/" + item.Name
			source := item.Source.Cluster + "/" + item.Source.Namespace + "/" + item.Source.Name
			if item.UID == "" || strings.TrimSpace(item.UID) != item.UID || item.Source.Validate() != nil ||
				item.Name != item.Source.Name || item.Namespace != RuntimeNamespace(item.Source.Cluster, item.Source.Namespace) ||
				HTTPSURL(item.Issuer, false) != nil || item.ClientID == "" || strings.ContainsAny(item.ClientID, "\r\n\t ") ||
				len(validation.IsDNS1123Subdomain(item.ClientSecretName)) != 0 ||
				uids[item.UID] || names[key] || sources[source] || sourceUIDs[item.Source.UID] {
				return nil, errors.New("invalid or duplicate enrollment mapping")
			}
			uids[item.UID], names[key], sources[source] = true, true, true
			sourceUIDs[item.Source.UID] = true
		}
	}
	pins := map[string]Entry{}
	for _, item := range doc.Pins {
		pins[item.UID] = item
	}
	for _, item := range doc.Entries {
		if pin, exists := pins[item.UID]; !exists || pin != item {
			return nil, errors.New("enrollment entry has no matching source pin")
		}
	}
	return &doc, nil
}

func (p *Policy) SelectEnrolled(object metav1.Object, reader io.Reader) (Entry, error) {
	doc, err := p.DecodeEnrollment(reader)
	if err != nil {
		return Entry{}, err
	}
	for _, entry := range doc.Entries {
		if entry.UID == string(object.GetUID()) && entry.Namespace == object.GetNamespace() && entry.Name == object.GetName() {
			if !p.Enrollment.Allows(entry) || p.staticConflict(entry) {
				return Entry{}, ErrUnmapped
			}
			return entry, nil
		}
	}
	return Entry{}, ErrUnmapped
}

func (p *Policy) staticConflict(entry Entry) bool {
	for _, fixed := range p.Entries {
		if (fixed.UID == entry.UID || fixed.Namespace == entry.Namespace && fixed.Name == entry.Name ||
			fixed.Source.UID == entry.Source.UID || fixed.Source.Cluster == entry.Source.Cluster &&
			fixed.Source.Namespace == entry.Source.Namespace && fixed.Source.Name == entry.Source.Name) && fixed != entry {
			return true
		}
	}
	return false
}
