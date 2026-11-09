// Package enrollment is the trusted platform enroller for ChatUIInstances in
// explicitly approved KCP logical clusters. It is not tenant code and holds no
// Keycloak admin or client-secret credentials. Its only writes are:
//
//   - the platform-owned enrollment ConfigMap (never created here), and
//   - validRedirectUris/webOrigins of named, pre-existing Crossplane Clients
//     that were explicitly opted in and have no other field owner.
//
// Identity comes only from the operator Policy, the KCP-assigned
// kcp.io/cluster annotation of each listed source, and the trusted MSP API.
// Tenant labels, other annotations, spec and status are never inputs.
//
// Callback registration is additive only: existing safe callbacks and origins
// are preserved and nothing is ever removed here. Callbacks of retired
// instances therefore stay registered; pruning them requires a separate,
// reviewed action. Authorization does not depend on the registered list: it is
// decided by the enrollment document, which pins the source UID.
package enrollment

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/apeirora/showroom-msp-chat-ui/internal/accesspolicy"
	"github.com/apeirora/showroom-msp-chat-ui/internal/hostnamepolicy"
)

const (
	// ManagedByLabel/ManagedByValue must be present on the enrollment ConfigMap.
	ManagedByLabel = "app.kubernetes.io/managed-by"
	ManagedByValue = "chat-ui-enrollment"
	// OwnedCallbacksAnnotation is the explicit opt-in on a Crossplane Client that
	// every other manager has relinquished validRedirectUris and webOrigins to this enroller.
	OwnedCallbacksAnnotation = "chatui.msp.apeirora.eu/enrollment-owned-callbacks"

	ClientAPIVersion   = "openidclient.keycloak.crossplane.io/v1alpha1"
	ClientKind         = "Client"
	InstanceAPIVersion = "ui.privatellms.msp/v1alpha1"
	InstanceKind       = "ChatUIInstance"
	ClusterAnnotation  = "kcp.io/cluster"
	CallbackPath       = "/oauth2/callback"

	// ProbeBatchSize bounds the callback probes of one pass (sequential, so at most
	// ProbeBatchSize verifier timeouts).
	ProbeBatchSize = 8

	// fieldManager is the only managedFields owner allowed on the two callback
	// fields; it matches the field manager used by the Client registry adapter.
	fieldManager = "chat-ui-enrollment"

	maxDocumentEntries     = 1000 // mirrors accesspolicy.DecodeEnrollment
	maxRegisteredCallbacks = 1000 // additive retention must not grow Client objects without bound
	maxSourcesPerWorkspace = 1000 // mirrors the source adapter; bounds reads per inventory pass
)

// SourceReader returns the COMPLETE list of ChatUIInstances of one KCP cluster.
// Adapters must follow pagination; a partial list must be returned as an error.
type SourceReader interface {
	ListSources(ctx context.Context, cluster string) ([]unstructured.Unstructured, error)
}

// ManagedReader reads the MSP-side ChatUIInstance from the trusted API.
// A missing object must surface as an apierrors NotFound error.
type ManagedReader interface {
	GetManaged(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error)
	// HasRuntime checks for a Deployment or Ingress at the deterministic workload
	// name. Only authoritative NotFound proves absence; every other error fails.
	HasRuntime(ctx context.Context, namespace, name string) (bool, error)
}

// Store reads and updates (never creates) the enrollment ConfigMap. The update
// must be optimistic: it must fail if UID or resourceVersion of original changed.
type Store interface {
	GetEnrollment(ctx context.Context) (*corev1.ConfigMap, error)
	UpdateEnrollment(ctx context.Context, original *corev1.ConfigMap, doc accesspolicy.EnrollmentDocument) error
}

// ClientRegistry accesses cluster-scoped Crossplane Keycloak Clients. UpdateRedirects
// may change spec.forProvider.validRedirectUris and webOrigins only, optimistically
// against original, and must not force field ownership.
type ClientRegistry interface {
	GetClient(ctx context.Context, name string) (*unstructured.Unstructured, error)
	UpdateRedirects(ctx context.Context, original *unstructured.Unstructured, redirects, origins []string) error
}

// CallbackVerifier proves that the identity provider currently accepts callback
// for clientID (registration through Crossplane/Keycloak may be asynchronous).
type CallbackVerifier interface {
	Verify(ctx context.Context, issuer, clientID, callback string) error
}

// ClientConfig names one existing Crossplane Client and the exact identity it must have.
type ClientConfig struct {
	Name               string `json:"name"`
	UID                string `json:"uid"`
	Realm              string `json:"realm"`
	Issuer             string `json:"issuer"`
	ClientID           string `json:"clientID"`
	ProviderConfigName string `json:"providerConfigName"`
	// BaseRedirectURIs are reviewed, always-registered callbacks (for example
	// static mappings). Each must satisfy the same callback rules as derived ones.
	BaseRedirectURIs []string `json:"baseRedirectURIs"`
}

type Config struct {
	Policy     *accesspolicy.Policy
	Hostnames  *hostnamepolicy.Policy
	PublicHost string // DNS base such as "chat.example.com"; callbacks are always https
	Clients    []ClientConfig
}

// Result is bounded: it contains counts only.
//
// In an apply run, Entries and Pins describe the document that was (or, when
// MappingUpdatePlanned is false, already is) stored: the published subset.
// In a dry run nothing is probed, so Entries and Pins describe the document
// that would be stored if every one of the ProbesPlanned probes succeeds, and
// Pending counts only candidates deferred beyond the per-pass probe bound.
type Result struct {
	Changed        bool // enrollment ConfigMap was written
	Entries        int
	Pins           int
	ClientsUpdated int
	// Skipped counts instances not enrolled: invalid source identity, missing MSP
	// object, hostname policy, pin or static conflict, and unproven binding order.
	// InvalidSources and StaleBindings are subsets of Skipped.
	Skipped        int
	InvalidSources int // sources with an invalid identity
	StaleBindings  int // first bindings refused: MSP object older than (or unproven against) its source

	// Pending counts accepted candidates whose callback is registered but not
	// published in this pass: failed probes plus candidates deferred by ProbeBatchSize.
	Pending       int
	ProbesPlanned int // callback probes selected for this pass (at most ProbeBatchSize)
	Probed        int // callback probes actually executed

	MappingUpdatePlanned bool
	ClientUpdatesPlanned int
	// CallbackAdditionsPlanned counts redirect URIs that are not registered yet
	// and would be added. Nothing is ever removed.
	CallbackAdditionsPlanned int
}

// Engine is not safe for concurrent Reconcile calls.
type Engine struct {
	Config   Config
	Sources  SourceReader
	Managed  ManagedReader
	Store    Store
	Registry ClientRegistry
	Verifier CallbackVerifier
	DryRun   bool // Inventory and validate only; never register callbacks or publish a mapping.
	// ProbeOffset rotates which not-yet-published candidates are probed first
	// (start index = ProbeOffset modulo their count). The caller increments it
	// after every pass so that unready candidates cannot starve the others.
	ProbeOffset int
}

type clientKey struct{ issuer, clientID string }

type candidate struct {
	entry    accesspolicy.Entry
	callback string
	key      clientKey

	sourceCreated  time.Time
	managedCreated time.Time
	priorState     bool // Used only to deny a first binding, never to grant access.
}

// orderProven reports whether the MSP object is not older than its source. The
// creation order is only a plausibility check against binding a stale MSP object
// to a recreated source; it is not proof of api-syncagent provenance.
func (c candidate) orderProven() bool {
	return !c.sourceCreated.IsZero() && !c.managedCreated.IsZero() && !c.managedCreated.Before(c.sourceCreated)
}

type clientPlan struct {
	original  *unstructured.Unstructured
	redirects []string
	origins   []string
	update    bool
}

type inventoryStats struct {
	skipped int // all instances skipped during inventory
	invalid int // subset of skipped: invalid source identity
}

// Reconcile validates the inventory and all registration targets before writes.
// It rereads source identities after registration and probing, before publishing
// any access. A callback that is not accepted yet delays only its own entry.
func (e *Engine) Reconcile(ctx context.Context) (Result, error) {
	var result Result
	clients, err := e.validate()
	if err != nil {
		return result, err
	}
	policy := e.Config.Policy

	configMap, current, err := e.readEnrollment(ctx)
	if err != nil {
		return result, err
	}
	first, stats, err := e.inventory(ctx, current.Pins)
	if err != nil {
		return result, err
	}
	pins, err := e.livePins(ctx, current.Pins)
	if err != nil {
		return result, err
	}
	accepted, conflicted, stale := bind(first, pins, policy.Entries)
	result.InvalidSources, result.StaleBindings = stats.invalid, stale
	result.Skipped = stats.skipped + conflicted + stale

	// The maximal document (every accepted candidate published) must already be valid.
	if err := e.checkDocument(buildDocument(accepted, accepted, pins, current.Entries)); err != nil {
		return result, err
	}
	plans, additions, err := e.planClients(ctx, clients, accepted)
	if err != nil {
		return result, err
	}
	result.CallbackAdditionsPlanned = additions
	for _, plan := range plans {
		if plan.update {
			result.ClientUpdatesPlanned++
		}
	}

	// Already published, exactly unchanged candidates stay; only new or changed ones are probed.
	published := map[accesspolicy.Entry]bool{}
	for _, entry := range current.Entries {
		published[entry] = true
	}
	active := make([]candidate, 0, len(accepted))
	fresh := make([]candidate, 0, len(accepted))
	for _, c := range accepted {
		if published[c.entry] {
			active = append(active, c)
		} else {
			fresh = append(fresh, c)
		}
	}
	selected, deferred := selectProbes(fresh, e.ProbeOffset)
	result.ProbesPlanned = len(selected)

	if e.DryRun {
		result.Pending = len(deferred)
		planned := buildDocument(accepted, append(slices.Clone(active), selected...), pins, current.Entries)
		result.Entries, result.Pins = len(planned.Entries), len(planned.Pins)
		result.MappingUpdatePlanned = !sameDocument(current, planned)
		return result, nil
	}

	// Registration first: all allowed callbacks are registered before any is published.
	for _, plan := range plans {
		if !plan.update {
			continue
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := e.Registry.UpdateRedirects(ctx, plan.original.DeepCopy(), slices.Clone(plan.redirects), slices.Clone(plan.origins)); err != nil {
			return result, fmt.Errorf("update client redirects: %w", err)
		}
		result.ClientsUpdated++
	}

	// Registration may be asynchronous: probe sequentially, publish only accepted callbacks.
	for _, c := range selected {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		result.Probed++
		if err := e.Verifier.Verify(ctx, c.entry.Issuer, c.entry.ClientID, c.callback); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return result, ctxErr
			}
			result.Pending++
			continue
		}
		active = append(active, c)
	}
	result.Pending += len(deferred)

	doc := buildDocument(accepted, active, pins, current.Entries)
	if err := e.checkDocument(doc); err != nil {
		return result, err
	}
	result.Entries, result.Pins = len(doc.Entries), len(doc.Pins)
	result.MappingUpdatePlanned = !sameDocument(current, doc)
	if !result.MappingUpdatePlanned {
		return result, nil
	}

	// External waits may have outlived the identities read above: read them again
	// and publish nothing unless every identity behind a new pin or an active entry
	// is still exactly the same. A failed read aborts; it is never "empty".
	second, _, err := e.inventory(ctx, current.Pins)
	if err != nil {
		return result, err
	}
	still := map[accesspolicy.Entry]bool{}
	secondAccepted, _, _ := bind(second, pins, policy.Entries)
	for _, c := range secondAccepted {
		still[c.entry] = true
	}
	for _, c := range affected(accepted, active, pins) {
		if !still[c.entry] {
			return result, fmt.Errorf("identity of %s/%s changed during reconciliation; retry", c.entry.Namespace, c.entry.Name)
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := e.Store.UpdateEnrollment(ctx, configMap.DeepCopy(), doc); err != nil {
		return result, fmt.Errorf("update enrollment ConfigMap: %w", err)
	}
	result.Changed = true
	return result, nil
}

// selectProbes rotates the (sorted) fresh candidates by offset modulo their
// count and splits them into at most ProbeBatchSize probes and the remainder.
func selectProbes(fresh []candidate, offset int) (selected, deferred []candidate) {
	n := len(fresh)
	if n == 0 {
		return nil, nil
	}
	start := ((offset % n) + n) % n
	rotated := append(slices.Clone(fresh[start:]), fresh[:start]...)
	batch := min(n, ProbeBatchSize)
	return rotated[:batch], rotated[batch:]
}

// affected lists the candidates whose identity must be unchanged at publication:
// those that become active entries and those that get a new pin.
func affected(considered, active []candidate, pins []accesspolicy.Entry) []candidate {
	isActive := map[accesspolicy.Entry]bool{}
	for _, c := range active {
		isActive[c.entry] = true
	}
	var out []candidate
	for _, c := range considered {
		if isActive[c.entry] || bindingIndex(pins, c.entry) < 0 {
			out = append(out, c)
		}
	}
	return out
}

// validate checks the whole configuration graph before any adapter is used.
func (e *Engine) validate() (map[clientKey]ClientConfig, error) {
	c := e.Config
	if e.Sources == nil || e.Managed == nil || e.Store == nil || e.Registry == nil || e.Verifier == nil {
		return nil, errors.New("enrollment engine requires all adapters")
	}
	if c.Policy == nil || c.Policy.Enrollment == nil {
		return nil, errors.New("automatic enrollment is not enabled")
	}
	if c.Hostnames == nil {
		return nil, errors.New("enrollment requires a hostname policy")
	}
	if err := c.Policy.Enrollment.Validate(); err != nil {
		return nil, err
	}
	if len(validation.IsDNS1123Label(c.Policy.CredentialsNamespace)) != 0 {
		return nil, errors.New("enrollment requires a credentials namespace")
	}
	if c.PublicHost == "" || strings.TrimSpace(c.PublicHost) != c.PublicHost {
		return nil, errors.New("enrollment requires an explicit PublicHost")
	}
	if err := c.Hostnames.ValidatePublicHost(c.PublicHost); err != nil {
		return nil, err
	}

	byKey := map[clientKey]ClientConfig{}
	names, uids := map[string]bool{}, map[string]bool{}
	for _, cc := range c.Clients {
		if err := validateClientConfig(cc); err != nil {
			return nil, err
		}
		key := clientKey{cc.Issuer, cc.ClientID}
		if names[cc.Name] || uids[cc.UID] {
			return nil, fmt.Errorf("duplicate enrollment client %q", cc.Name)
		}
		if _, exists := byKey[key]; exists {
			return nil, fmt.Errorf("duplicate enrollment client for issuer and client ID of %q", cc.Name)
		}
		for _, uri := range cc.BaseRedirectURIs {
			if _, err := parseCallback(c.PublicHost, uri); err != nil {
				return nil, fmt.Errorf("client %q base redirect: %w", cc.Name, err)
			}
		}
		names[cc.Name], uids[cc.UID], byKey[key] = true, true, cc
	}
	used := map[clientKey]bool{}
	for _, w := range c.Policy.Enrollment.Workspaces {
		key := clientKey{w.Issuer, w.ClientID}
		if _, exists := byKey[key]; !exists {
			return nil, fmt.Errorf("workspace %s has no matching enrollment client configuration", w.Cluster)
		}
		used[key] = true
	}
	for key, cc := range byKey {
		if !used[key] {
			return nil, fmt.Errorf("enrollment client %q matches no approved workspace", cc.Name)
		}
	}
	return byKey, nil
}

func validateClientConfig(cc ClientConfig) error {
	if len(validation.IsDNS1123Subdomain(cc.Name)) != 0 || !plainToken(cc.UID) || !plainToken(cc.Realm) || !plainToken(cc.ClientID) ||
		len(validation.IsDNS1123Subdomain(cc.ProviderConfigName)) != 0 || accesspolicy.HTTPSURL(cc.Issuer, false) != nil {
		return fmt.Errorf("invalid enrollment client configuration %q", cc.Name)
	}
	issuerURL, _ := url.Parse(cc.Issuer)
	if len(validation.IsDNS1123Subdomain(cc.Realm)) != 0 || !strings.HasSuffix(issuerURL.Path, "/realms/"+cc.Realm) {
		return fmt.Errorf("client %q issuer does not identify its configured Keycloak realm", cc.Name)
	}
	return nil
}

func plainToken(s string) bool {
	return s != "" && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\r\n\t ")
}

// parseCallback accepts exactly https://<one-slug-label>.<PublicHost>/oauth2/callback
// and returns its origin. Everything else (wildcards, ports, query, fragment,
// userinfo, other paths, nested labels) is rejected by exact reconstruction.
func parseCallback(publicHost, raw string) (string, error) {
	invalid := fmt.Errorf("callback %q is not an exact https slug callback under the public host", raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", invalid
	}
	slug, ok := strings.CutSuffix(u.Host, "."+publicHost)
	if !ok || slug == "" || strings.Contains(slug, ".") {
		return "", invalid
	}
	if _, err := hostnamepolicy.Host(publicHost, slug); err != nil {
		return "", invalid
	}
	origin := "https://" + u.Host
	if raw != origin+CallbackPath {
		return "", invalid
	}
	return origin, nil
}

func (e *Engine) readEnrollment(ctx context.Context) (*corev1.ConfigMap, *accesspolicy.EnrollmentDocument, error) {
	policy := e.Config.Policy
	cm, err := e.Store.GetEnrollment(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("read enrollment ConfigMap: %w", err)
	}
	if cm == nil || cm.Namespace != policy.CredentialsNamespace || cm.Name != policy.Enrollment.ConfigMapName ||
		cm.UID == "" || cm.ResourceVersion == "" || cm.DeletionTimestamp != nil || cm.Labels[ManagedByLabel] != ManagedByValue {
		return nil, nil, errors.New("enrollment ConfigMap is missing, not the configured object, deleting, or not labeled as managed by the enroller")
	}
	raw, ok := cm.Data[accesspolicy.EnrollmentDataKey]
	if !ok {
		return nil, nil, errors.New("enrollment ConfigMap has no " + accesspolicy.EnrollmentDataKey)
	}
	doc, err := policy.DecodeEnrollment(strings.NewReader(raw))
	if err != nil {
		return nil, nil, fmt.Errorf("enrollment ConfigMap content is invalid: %w", err)
	}
	return cm, doc, nil
}

func sourceIdentity(item *unstructured.Unstructured, cluster string) (accesspolicy.Source, error) {
	if item.GetAPIVersion() != InstanceAPIVersion || item.GetKind() != InstanceKind {
		return accesspolicy.Source{}, fmt.Errorf("source in workspace %s has an unexpected apiVersion or kind", cluster)
	}
	if item.GetAnnotations()[ClusterAnnotation] != cluster {
		return accesspolicy.Source{}, fmt.Errorf("source %q was not assigned by KCP to workspace %s", item.GetName(), cluster)
	}
	source := accesspolicy.Source{Cluster: cluster, Namespace: item.GetNamespace(), Name: item.GetName(), UID: string(item.GetUID())}
	if err := source.Validate(); err != nil {
		return accesspolicy.Source{}, fmt.Errorf("source %q in workspace %s: %w", item.GetName(), cluster, err)
	}
	return source, nil
}

// managedIdentity verifies the trusted object is exactly the requested one and returns its UID.
func managedIdentity(object *unstructured.Unstructured, namespace, name string) (string, error) {
	uid := ""
	if object != nil {
		uid = string(object.GetUID())
	}
	if object == nil || object.GetAPIVersion() != InstanceAPIVersion || object.GetKind() != InstanceKind ||
		object.GetNamespace() != namespace || object.GetName() != name || uid == "" || strings.TrimSpace(uid) != uid {
		return "", fmt.Errorf("managed object %s/%s does not have the requested identity", namespace, name)
	}
	return uid, nil
}

// inventory lists every approved workspace completely and resolves the trusted
// MSP object of each live source. List and read failures abort: they are never
// "empty". A source with an invalid identity is skipped and counted; duplicates
// and inconsistent MSP identities cannot come from a correct API and abort.
func (e *Engine) inventory(ctx context.Context, pins []accesspolicy.Entry) ([]candidate, inventoryStats, error) {
	policy := e.Config.Policy
	var out []candidate
	var stats inventoryStats
	sourceSeen, sourceUIDs := map[string]bool{}, map[string]bool{}
	localSeen, managedUIDs := map[string]bool{}, map[string]bool{}
	for _, w := range policy.Enrollment.Workspaces {
		if err := ctx.Err(); err != nil {
			return nil, inventoryStats{}, err
		}
		items, err := e.Sources.ListSources(ctx, w.Cluster)
		if err != nil {
			return nil, inventoryStats{}, fmt.Errorf("list ChatUIInstances in workspace %s: %w", w.Cluster, err)
		}
		if len(items) > maxSourcesPerWorkspace {
			return nil, inventoryStats{}, fmt.Errorf("workspace %s exceeds %d ChatUIInstances", w.Cluster, maxSourcesPerWorkspace)
		}
		for i := range items {
			item := &items[i]
			source, err := sourceIdentity(item, w.Cluster)
			if err != nil {
				stats.skipped++
				stats.invalid++
				continue
			}
			coordinates := source.Cluster + "/" + source.Namespace + "/" + source.Name
			if sourceSeen[coordinates] || sourceUIDs[source.UID] {
				return nil, inventoryStats{}, fmt.Errorf("duplicate source %s", coordinates)
			}
			sourceSeen[coordinates], sourceUIDs[source.UID] = true, true
			if item.GetDeletionTimestamp() != nil {
				continue
			}

			namespace := accesspolicy.RuntimeNamespace(w.Cluster, source.Namespace)
			object, err := e.Managed.GetManaged(ctx, namespace, source.Name)
			if apierrors.IsNotFound(err) {
				stats.skipped++
				continue
			}
			if err != nil {
				return nil, inventoryStats{}, fmt.Errorf("read managed object %s/%s: %w", namespace, source.Name, err)
			}
			managedUID, err := managedIdentity(object, namespace, source.Name)
			if err != nil {
				return nil, inventoryStats{}, err
			}
			if object.GetCreationTimestamp().Time.IsZero() {
				return nil, inventoryStats{}, fmt.Errorf("managed object %s/%s has no creation timestamp", namespace, source.Name)
			}
			if object.GetDeletionTimestamp() != nil {
				continue
			}
			local := namespace + "/" + source.Name
			if localSeen[local] || managedUIDs[managedUID] {
				return nil, inventoryStats{}, fmt.Errorf("duplicate managed identity %s", local)
			}
			localSeen[local], managedUIDs[managedUID] = true, true

			// The existing hostname policy decides; no cutover or slug rules live here.
			selection, err := e.Config.Hostnames.Select(object)
			if err != nil {
				stats.skipped++
				continue
			}
			host, err := hostnamepolicy.Host(e.Config.PublicHost, selection.Slug)
			if err != nil {
				return nil, inventoryStats{}, err
			}
			entry := accesspolicy.Entry{UID: managedUID, Namespace: namespace, Name: source.Name, Source: source,
				Issuer: w.Issuer, ClientID: w.ClientID, ClientSecretName: w.ClientSecretName}
			if !policy.Enrollment.Allows(entry) {
				return nil, inventoryStats{}, fmt.Errorf("derived mapping for %s is not allowed by the enrollment policy", local)
			}
			priorState, err := e.priorRuntime(ctx, object, entry, pins)
			if err != nil {
				return nil, inventoryStats{}, err
			}
			callback := "https://" + host + CallbackPath
			if _, err := parseCallback(e.Config.PublicHost, callback); err != nil {
				return nil, inventoryStats{}, err
			}
			out = append(out, candidate{entry: entry, callback: callback, key: clientKey{w.Issuer, w.ClientID},
				sourceCreated: item.GetCreationTimestamp().Time, managedCreated: object.GetCreationTimestamp().Time, priorState: priorState})
		}
	}
	slices.SortFunc(out, func(a, b candidate) int { return compareEntries(a.entry, b.entry) })
	return out, stats, nil
}

// A timestamp is not sync provenance: it may have second precision or clock
// skew. A first automatic binding therefore also requires an unused runtime.
// Existing pins and exact reviewed static entries bypass this check. Status is
// consulted only as a reason to refuse adoption, never as authorization evidence.
func (e *Engine) priorRuntime(ctx context.Context, object *unstructured.Unstructured, entry accesspolicy.Entry, pins []accesspolicy.Entry) (bool, error) {
	bound, clash := pinState(entry, pins)
	if bound || clash || slices.Contains(e.Config.Policy.Entries, entry) {
		return false, nil
	}
	keys, _, err := unstructured.NestedStringMap(object.Object, "status", "accessSecretUIDs")
	if err != nil || len(keys) != 0 {
		return true, nil
	}
	exists, err := e.Managed.HasRuntime(ctx, entry.Namespace, entry.Name)
	if err != nil {
		return false, fmt.Errorf("check existing runtime for %s/%s: %w", entry.Namespace, entry.Name, err)
	}
	return exists, nil
}

func compareEntries(a, b accesspolicy.Entry) int {
	return cmp.Or(strings.Compare(a.Namespace, b.Namespace), strings.Compare(a.Name, b.Name))
}

// livePins keeps a pin until its exact MSP coordinates are proven NotFound or
// to hold a different UID. Any other read failure aborts the run. Pins do not
// depend on the current workspace allowlist: they outlive removed workspaces
// for as long as the MSP object survives.
func (e *Engine) livePins(ctx context.Context, pins []accesspolicy.Entry) ([]accesspolicy.Entry, error) {
	kept := []accesspolicy.Entry{}
	for _, pin := range pins {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		object, err := e.Managed.GetManaged(ctx, pin.Namespace, pin.Name)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read pinned managed object %s/%s: %w", pin.Namespace, pin.Name, err)
		}
		uid, err := managedIdentity(object, pin.Namespace, pin.Name)
		if err != nil {
			return nil, err
		}
		if uid == pin.UID {
			kept = append(kept, pin)
		}
	}
	return kept, nil
}

// sameBinding compares the SOURCE and MSP identity only, not issuer, client or secret.
func sameBinding(a, b accesspolicy.Entry) bool {
	return a.UID == b.UID && a.Namespace == b.Namespace && a.Name == b.Name && a.Source == b.Source
}

// sharesIdentity: the managed UID, source UID, local coordinates or source coordinates overlap.
func sharesIdentity(a, b accesspolicy.Entry) bool {
	return a.UID == b.UID || a.Source.UID == b.Source.UID ||
		a.Namespace == b.Namespace && a.Name == b.Name ||
		a.Source.Cluster == b.Source.Cluster && a.Source.Namespace == b.Source.Namespace && a.Source.Name == b.Source.Name
}

// conflicts reports a disagreement with a static entry: sharing an identity with
// an entry that is not exactly identical (full equality).
func conflicts(entry accesspolicy.Entry, existing []accesspolicy.Entry) bool {
	for _, item := range existing {
		if item != entry && sharesIdentity(item, entry) {
			return true
		}
	}
	return false
}

// bindingIndex returns the index of the pin with the same source+MSP binding, or -1.
func bindingIndex(pins []accesspolicy.Entry, entry accesspolicy.Entry) int {
	return slices.IndexFunc(pins, func(pin accesspolicy.Entry) bool { return sameBinding(pin, entry) })
}

// pinState compares a candidate with surviving pins by binding identity only, so
// a reviewed issuer/client/secret change of the SAME source and MSP object is not
// a conflict, while any other overlap is.
func pinState(entry accesspolicy.Entry, pins []accesspolicy.Entry) (bound, clash bool) {
	for _, pin := range pins {
		if sameBinding(pin, entry) {
			bound = true
		} else if sharesIdentity(pin, entry) {
			clash = true
		}
	}
	return bound, clash
}

// bind rejects candidates that disagree with static mappings or surviving pins.
// Exact duplicates of a static entry are permitted. A first binding (no pin yet)
// additionally requires a nonzero source creation time and an MSP object that is
// not older than its source; an already pinned binding is protected by its pin.
func bind(candidates []candidate, pins, static []accesspolicy.Entry) (accepted []candidate, conflicted, stale int) {
	for _, c := range candidates {
		if conflicts(c.entry, static) {
			conflicted++
			continue
		}
		bound, clash := pinState(c.entry, pins)
		if clash {
			conflicted++
			continue
		}
		reviewed := slices.Contains(static, c.entry)
		if !bound && !reviewed && (!c.orderProven() || c.priorState) {
			stale++
			continue
		}
		accepted = append(accepted, c)
	}
	return accepted, conflicted, stale
}

func sortedEntries(in []accesspolicy.Entry) []accesspolicy.Entry {
	out := append([]accesspolicy.Entry{}, in...)
	slices.SortFunc(out, compareEntries)
	return out
}

// buildDocument publishes the active candidates as entries. Every considered
// candidate (published, verified or still pending) is pinned so that a source
// recreated between passes cannot rebind the surviving MSP object. A pin of the
// same binding is replaced, never duplicated. A considered candidate that is not
// active keeps its previously published entry (if any) unchanged until the new
// configuration is verified.
func buildDocument(considered, active []candidate, pins, previous []accesspolicy.Entry) accesspolicy.EnrollmentDocument {
	entries := []accesspolicy.Entry{}
	allPins := append([]accesspolicy.Entry{}, pins...)
	isActive := map[accesspolicy.Entry]bool{}
	for _, c := range active {
		isActive[c.entry] = true
		entries = append(entries, c.entry)
		if i := bindingIndex(allPins, c.entry); i >= 0 {
			allPins[i] = c.entry
		} else {
			allPins = append(allPins, c.entry)
		}
	}
	for _, c := range considered {
		if isActive[c.entry] {
			continue
		}
		i := bindingIndex(allPins, c.entry)
		if i < 0 {
			allPins = append(allPins, c.entry)
		} else if slices.Contains(previous, allPins[i]) {
			entries = append(entries, allPins[i])
		}
	}
	return accesspolicy.EnrollmentDocument{Version: 1, Entries: sortedEntries(entries), Pins: sortedEntries(allPins)}
}

// checkDocument applies the size bound and the production decoder to the exact bytes to be stored.
func (e *Engine) checkDocument(doc accesspolicy.EnrollmentDocument) error {
	if len(doc.Entries) > maxDocumentEntries || len(doc.Pins) > maxDocumentEntries {
		return fmt.Errorf("enrollment would exceed %d entries or pins", maxDocumentEntries)
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	if _, err := e.Config.Policy.DecodeEnrollment(bytes.NewReader(data)); err != nil {
		return fmt.Errorf("computed enrollment is invalid: %w", err)
	}
	return nil
}

func canonical(doc accesspolicy.EnrollmentDocument) []byte {
	doc.Entries, doc.Pins = sortedEntries(doc.Entries), sortedEntries(doc.Pins)
	data, err := json.Marshal(doc)
	if err != nil {
		return nil
	}
	return data
}

func sameDocument(current *accesspolicy.EnrollmentDocument, doc accesspolicy.EnrollmentDocument) bool {
	old := canonical(*current)
	return old != nil && bytes.Equal(old, canonical(doc))
}

func sortedUnique(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return slices.Compact(out)
}

// planClients reads and validates EVERY configured Client before any write. The
// plan is additive: existing callbacks and origins are kept only if all of them
// satisfy the exact callback rules; any malformed or foreign value refuses the
// whole run. The result is the union of existing, base and newly derived values.
// It returns the number of redirect URIs that would be added.
func (e *Engine) planClients(ctx context.Context, clients map[clientKey]ClientConfig, accepted []candidate) ([]clientPlan, int, error) {
	callbacks := map[clientKey][]string{}
	for _, c := range accepted {
		callbacks[c.key] = append(callbacks[c.key], c.callback)
	}
	ordered := make([]ClientConfig, 0, len(clients))
	for _, cc := range clients {
		ordered = append(ordered, cc)
	}
	slices.SortFunc(ordered, func(a, b ClientConfig) int { return strings.Compare(a.Name, b.Name) })

	plans := make([]clientPlan, 0, len(ordered))
	additions := 0
	for _, cc := range ordered {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		object, err := e.Registry.GetClient(ctx, cc.Name)
		if err != nil {
			return nil, 0, fmt.Errorf("read client %s: %w", cc.Name, err)
		}
		if err := validateClient(object, cc); err != nil {
			return nil, 0, err
		}
		currentRedirects, _, err := unstructured.NestedStringSlice(object.Object, "spec", "forProvider", "validRedirectUris")
		if err != nil {
			return nil, 0, fmt.Errorf("client %s validRedirectUris is malformed", cc.Name)
		}
		currentOrigins, _, err := unstructured.NestedStringSlice(object.Object, "spec", "forProvider", "webOrigins")
		if err != nil {
			return nil, 0, fmt.Errorf("client %s webOrigins is malformed", cc.Name)
		}
		for _, redirect := range currentRedirects {
			if _, err := parseCallback(e.Config.PublicHost, redirect); err != nil {
				return nil, 0, fmt.Errorf("client %s has an existing redirect URI that is not an approved callback; refusing to modify it", cc.Name)
			}
		}
		for _, origin := range currentOrigins {
			if _, err := parseCallback(e.Config.PublicHost, origin+CallbackPath); err != nil {
				return nil, 0, fmt.Errorf("client %s has an existing web origin that is not an approved origin; refusing to modify it", cc.Name)
			}
		}

		redirects := slices.Clone(currentRedirects)
		redirects = append(redirects, cc.BaseRedirectURIs...)
		redirects = sortedUnique(append(redirects, callbacks[clientKey{cc.Issuer, cc.ClientID}]...))
		if len(redirects) > maxRegisteredCallbacks {
			return nil, 0, errors.New("client callback limit reached; reviewed retirement cleanup required")
		}
		origins := slices.Clone(currentOrigins)
		for _, redirect := range redirects {
			origin, err := parseCallback(e.Config.PublicHost, redirect)
			if err != nil {
				return nil, 0, err
			}
			origins = append(origins, origin)
		}
		origins = sortedUnique(origins)

		added := 0
		for _, redirect := range redirects {
			if !slices.Contains(currentRedirects, redirect) {
				added++
			}
		}
		addedOrigins := 0
		for _, origin := range origins {
			if !slices.Contains(currentOrigins, origin) {
				addedOrigins++
			}
		}
		additions += added
		plans = append(plans, clientPlan{original: object, redirects: redirects, origins: origins, update: added > 0 || addedOrigins > 0})
	}
	return plans, additions, nil
}

func stringAt(object *unstructured.Unstructured, want string, path ...string) error {
	got, found, err := unstructured.NestedString(object.Object, path...)
	if err != nil || !found || got != want {
		return fmt.Errorf("client %s: %s does not have the required value", object.GetName(), strings.Join(path, "."))
	}
	return nil
}

// boolAt: required settings must be present; optional false settings may be absent.
func boolAt(object *unstructured.Unstructured, want, required bool, field string) error {
	got, found, err := unstructured.NestedBool(object.Object, "spec", "forProvider", field)
	if err != nil || (!found && (required || want)) || (found && got != want) {
		return fmt.Errorf("client %s: %s does not have the required value", object.GetName(), field)
	}
	return nil
}

func validateClient(object *unstructured.Unstructured, cc ClientConfig) error {
	if object == nil || object.GetAPIVersion() != ClientAPIVersion || object.GetKind() != ClientKind || object.GetName() != cc.Name ||
		object.GetNamespace() != "" || string(object.GetUID()) != cc.UID || object.GetDeletionTimestamp() != nil {
		return fmt.Errorf("client %s does not have the configured identity", cc.Name)
	}
	if object.GetAnnotations()[OwnedCallbacksAnnotation] != "true" {
		return fmt.Errorf("client %s has not opted in to enrollment-owned callbacks", cc.Name)
	}
	for _, check := range []error{
		stringAt(object, cc.ProviderConfigName, "spec", "providerConfigRef", "name"),
		stringAt(object, cc.Realm, "spec", "forProvider", "realmId"),
		stringAt(object, cc.ClientID, "spec", "forProvider", "clientId"),
		stringAt(object, "CONFIDENTIAL", "spec", "forProvider", "accessType"),
		stringAt(object, "S256", "spec", "forProvider", "pkceCodeChallengeMethod"),
		boolAt(object, true, true, "enabled"),
		boolAt(object, true, true, "standardFlowEnabled"),
		boolAt(object, false, false, "directAccessGrantsEnabled"),
		boolAt(object, false, false, "implicitFlowEnabled"),
		boolAt(object, false, false, "serviceAccountsEnabled"),
		boolAt(object, false, false, "standardTokenExchangeEnabled"),
	} {
		if check != nil {
			return check
		}
	}
	owner, err := foreignCallbackOwner(object)
	if err != nil {
		return fmt.Errorf("client %s: %w", cc.Name, err)
	}
	if owner != "" {
		return fmt.Errorf("client %s: field manager %q still owns validRedirectUris or webOrigins; it must relinquish them", cc.Name, owner)
	}
	return nil
}

// foreignCallbackOwner returns the first managedFields manager other than the
// enroller itself that owns spec.forProvider.validRedirectUris or webOrigins.
// No manager name (Flux, Helm, Argo, kubectl, a Crossplane provider, ...) is
// trusted: all of them must relinquish both fields. Missing managedFields and
// ownership of other fields (for example status only) are allowed. Everything
// is read from the raw object, because typed accessors silently drop malformed
// data, which would fail open; malformed ownership data is an error.
func foreignCallbackOwner(object *unstructured.Unstructured) (string, error) {
	malformed := errors.New("managedFields is malformed")
	raw, found, err := unstructured.NestedFieldNoCopy(object.Object, "metadata", "managedFields")
	if err != nil {
		return "", malformed
	}
	if !found || raw == nil {
		return "", nil
	}
	var items []any
	switch list := raw.(type) {
	case []any:
		items = list
	case []map[string]any:
		for _, item := range list {
			items = append(items, item)
		}
	default:
		return "", malformed
	}
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			return "", malformed
		}
		manager, ok := entry["manager"].(string)
		if !ok || manager == "" {
			return "", malformed
		}
		var fields map[string]any
		switch value := entry["fieldsV1"].(type) {
		case nil:
		case map[string]any:
			fields = value
		case string:
			if err := json.Unmarshal([]byte(value), &fields); err != nil {
				return "", malformed
			}
		default:
			return "", malformed
		}
		owns, err := callbackFieldsOwned(fields)
		if err != nil {
			return "", malformed
		}
		if owns && manager != fieldManager {
			return manager, nil
		}
	}
	return "", nil
}

// Empty field-set leaves represent ownership of an entire atomic field. An
// owner of spec or forProvider must therefore also relinquish that parent before
// the enroller can own callbacks. Validate callback leaves even for our manager.
func callbackFieldsOwned(fields map[string]any) (bool, error) {
	current := fields
	for _, key := range []string{"f:spec", "f:forProvider"} {
		value, present := current[key]
		if !present {
			return false, nil
		}
		next, ok := value.(map[string]any)
		if !ok {
			return false, errors.New("malformed fieldsV1")
		}
		if len(next) == 0 {
			return true, nil
		}
		if dot, present := next["."]; present {
			self, valid := dot.(map[string]any)
			if !valid || len(self) != 0 {
				return false, errors.New("malformed fieldsV1 self marker")
			}
			if len(next) == 1 {
				return true, nil
			}
		}
		current = next
	}
	owned := false
	for _, key := range []string{"f:validRedirectUris", "f:webOrigins"} {
		if value, present := current[key]; present {
			if !validFieldsTree(value) {
				return false, errors.New("malformed callback fieldsV1")
			}
			owned = true
		}
	}
	return owned, nil
}

func validFieldsTree(value any) bool {
	fields, ok := value.(map[string]any)
	if !ok {
		return false
	}
	for _, child := range fields {
		if !validFieldsTree(child) {
			return false
		}
	}
	return true
}
