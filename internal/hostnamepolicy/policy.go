// Package hostnamepolicy selects names from operator configuration and immutable
// service-cluster identity. Tenant annotations, labels and status are not inputs.
package hostnamepolicy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	Legacy     = "Legacy"
	Derived    = "Derived"
	legacyMode = "legacy"
	deriveMode = "derive"
)

var (
	ErrLegacyUnmapped = errors.New("legacy instance has no reviewed hostname mapping")
	ErrIdentity       = errors.New("instance identity does not match the hostname policy")
	reserved          = regexp.MustCompile(`^ui-[a-f0-9]{32}$`)
)

type configuration struct {
	Cutover string  `json:"cutover"`
	Entries []entry `json:"entries"`
}

type entry struct {
	UID       string `json:"uid"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Mode      string `json:"mode"`
	Slug      string `json:"slug,omitempty"`
}

// Policy is immutable after parsing. Changes require a coordinated restart.
type Policy struct {
	cutover time.Time
	entries map[string]entry
}

type Selection struct {
	Slug   string
	Source string
}

// Host validates the complete DNS name, including each label and total length.
func Host(base, slug string) (string, error) {
	host := slug + "." + strings.TrimSpace(base)
	if len(validation.IsDNS1123Label(slug)) != 0 || len(validation.IsDNS1123Subdomain(host)) != 0 {
		return "", errors.New("PUBLIC_HOST and selected slug must form a valid DNS hostname")
	}
	for _, label := range strings.Split(host, ".") {
		if len(validation.IsDNS1123Label(label)) != 0 {
			return "", errors.New("PUBLIC_HOST and selected slug must form a valid DNS hostname")
		}
	}
	return host, nil
}

// ValidatePublicHost checks every possible configured name before controllers start.
func (p *Policy) ValidatePublicHost(base string) error {
	if _, err := Host(base, "ui-"+strings.Repeat("a", 32)); err != nil {
		return err
	}
	for _, item := range p.entries {
		if item.Mode == legacyMode {
			if _, err := Host(base, item.Slug); err != nil {
				return err
			}
		}
	}
	return nil
}

// Parse rejects ambiguous or incomplete migration configuration at startup.
func Parse(reader io.Reader) (*Policy, error) {
	data, err := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
	if err != nil {
		return nil, fmt.Errorf("read hostname policy: %w", err)
	}
	if len(data) > 1<<20 {
		return nil, errors.New("hostname policy exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var config configuration
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("decode hostname policy: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("hostname policy must contain exactly one JSON document")
	}
	cutover, err := time.Parse(time.RFC3339, config.Cutover)
	if err != nil || cutover.IsZero() {
		return nil, errors.New("hostname policy requires a fixed RFC3339 cutover timestamp")
	}
	policy := &Policy{cutover: cutover, entries: make(map[string]entry, len(config.Entries))}
	slugs := make(map[string]bool, len(config.Entries))
	identities := make(map[string]bool, len(config.Entries))
	for _, item := range config.Entries {
		if err := validateEntry(item); err != nil {
			return nil, err
		}
		identity := item.Namespace + "/" + item.Name
		if _, exists := policy.entries[item.UID]; exists || identities[identity] {
			return nil, errors.New("hostname policy contains duplicate instance identity")
		}
		if item.Mode == legacyMode && slugs[item.Slug] {
			return nil, errors.New("hostname policy contains duplicate legacy slug")
		}
		policy.entries[item.UID] = item
		identities[identity] = true
		if item.Mode == legacyMode {
			slugs[item.Slug] = true
		}
	}
	return policy, nil
}

func validateEntry(item entry) error {
	if item.UID == "" || strings.TrimSpace(item.UID) != item.UID ||
		len(validation.IsDNS1123Label(item.Namespace)) != 0 || len(validation.IsDNS1123Subdomain(item.Name)) != 0 {
		return errors.New("hostname policy entry requires UID, namespace and name")
	}
	switch item.Mode {
	case legacyMode:
		if len(validation.IsDNS1123Label(item.Slug)) != 0 || reserved.MatchString(item.Slug) {
			return errors.New("legacy slug must be a DNS label outside the reserved ui-<32 hex> format")
		}
	case deriveMode:
		if item.Slug != "" {
			return errors.New("derive entries must not supply a slug")
		}
	default:
		return errors.New("hostname policy entry mode must be legacy or derive")
	}
	return nil
}

// Select binds a mapping to UID, namespace and name. Unmapped objects created at
// or before the cutoff are never silently renamed. A post-cutoff restore with a
// new UID is indistinguishable from a new object: recovery requires a reviewed map.
func (p *Policy) Select(object metav1.Object) (Selection, error) {
	created := object.GetCreationTimestamp()
	if p == nil || object.GetUID() == "" || created.IsZero() {
		return Selection{}, ErrIdentity
	}
	uid := string(object.GetUID())
	if item, exists := p.entries[uid]; exists {
		if item.Namespace != object.GetNamespace() || item.Name != object.GetName() {
			return Selection{}, ErrIdentity
		}
		if item.Mode == legacyMode {
			return Selection{Slug: item.Slug, Source: Legacy}, nil
		}
	} else if !created.After(p.cutover) {
		return Selection{}, ErrLegacyUnmapped
	}
	digest := sha256.Sum256([]byte("ui.privatellms.msp/slug/v1:" + uid))
	return Selection{Slug: "ui-" + hex.EncodeToString(digest[:16]), Source: Derived}, nil
}
