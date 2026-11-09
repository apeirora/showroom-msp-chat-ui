package enrollment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/apeirora/showroom-msp-chat-ui/internal/accesspolicy"
	"github.com/apeirora/showroom-msp-chat-ui/internal/hostnamepolicy"
)

const (
	testOther    = "other"
	testIssuer   = "https://id.example.test/realms/showroom"
	publicHost   = "chat.example.test"
	baseCallback = "https://admin.chat.example.test/oauth2/callback"
	baseOrigin   = "https://admin.chat.example.test"
	emptyDoc     = `{"version":1,"entries":[],"pins":[]}`
)

var instances = schema.GroupResource{Group: "ui.privatellms.msp", Resource: "chatuiinstances"}

// world is one in-memory implementation of every adapter interface.
type world struct {
	sources map[string][]unstructured.Unstructured
	managed map[string]*unstructured.Unstructured

	listErr      error
	getErr       error // non-NotFound failure of GetManaged
	runtimeErr   error
	runtimes     map[string]bool
	runtimeReads int
	listed       []string
	listCalls    int
	onList       func(w *world, call int)

	cm       *corev1.ConfigMap
	cmWrites int

	client       *unstructured.Unstructured
	clientWrites int
	redirects    []string
	origins      []string

	verifyErr  error
	verifyFail map[string]error // per-callback failure, in addition to verifyErr
	verified   []string

	adapterCalls int
}

func newWorld(t *testing.T) (*world, *Engine) {
	t.Helper()
	hostnames, err := hostnamepolicy.Parse(strings.NewReader(`{"cutover":"2020-01-01T00:00:00Z","entries":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	policy := &accesspolicy.Policy{
		KCPServer: "https://kcp.example.test", CredentialsNamespace: "chat-system",
		Enrollment: &accesspolicy.Enrollment{ConfigMapName: "chat-enrollment", Workspaces: []accesspolicy.Workspace{
			{Cluster: "account-a", Issuer: testIssuer, ClientID: "chat-ui", ClientSecretName: "chat-oauth"}}},
	}
	w := &world{
		sources: map[string][]unstructured.Unstructured{}, managed: map[string]*unstructured.Unstructured{},
		cm: enrollmentMap(emptyDoc), client: keycloakClient(),
	}
	e := &Engine{
		Config: Config{Policy: policy, Hostnames: hostnames, PublicHost: publicHost, Clients: []ClientConfig{{
			Name: "chat-ui", UID: "client-uid", Realm: "showroom", Issuer: testIssuer, ClientID: "chat-ui",
			ProviderConfigName: "keycloak", BaseRedirectURIs: []string{baseCallback}}}},
		Sources: w, Managed: w, Store: w, Registry: w, Verifier: w,
	}
	return w, e
}

func enrollmentMap(doc string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "chat-enrollment", Namespace: "chat-system", UID: "cm-uid", ResourceVersion: "7",
			Labels: map[string]string{ManagedByLabel: ManagedByValue}},
		Data: map[string]string{accesspolicy.EnrollmentDataKey: doc},
	}
}

func keycloakClient() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": ClientAPIVersion, "kind": ClientKind,
		"metadata": map[string]any{"name": "chat-ui", "uid": "client-uid",
			"annotations": map[string]any{OwnedCallbacksAnnotation: "true"}},
		"spec": map[string]any{
			"providerConfigRef": map[string]any{"name": "keycloak"},
			"forProvider": map[string]any{
				"realmId": "showroom", "clientId": "chat-ui", "accessType": "CONFIDENTIAL",
				"enabled": true, "standardFlowEnabled": true, "directAccessGrantsEnabled": false,
				"pkceCodeChallengeMethod": "S256",
				"validRedirectUris":       []any{baseCallback}, "webOrigins": []any{baseOrigin},
			},
		},
	}}
}

func source(cluster, namespace, name, uid string) unstructured.Unstructured {
	return unstructured.Unstructured{Object: map[string]any{
		"apiVersion": InstanceAPIVersion, "kind": InstanceKind,
		"metadata": map[string]any{"name": name, "namespace": namespace, "uid": uid,
			"annotations": map[string]any{ClusterAnnotation: cluster}, "creationTimestamp": "2025-12-31T00:00:00Z"},
	}}
}

func managedObject(cluster, namespace, name, uid string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": InstanceAPIVersion, "kind": InstanceKind,
		"metadata": map[string]any{"name": name, "namespace": accesspolicy.RuntimeNamespace(cluster, namespace),
			"uid": uid, "creationTimestamp": "2026-01-01T00:00:00Z"},
	}}
}

func mspKey(cluster, namespace, name string) string {
	return accesspolicy.RuntimeNamespace(cluster, namespace) + "/" + name
}

func (w *world) add(cluster, namespace, name, sourceUID, managedUID string) {
	w.sources[cluster] = append(w.sources[cluster], source(cluster, namespace, name, sourceUID))
	w.managed[mspKey(cluster, namespace, name)] = managedObject(cluster, namespace, name, managedUID)
}

// slug and callbackFor are an independent restatement of the existing derived-hostname rule.
func slug(managedUID string) string {
	digest := sha256.Sum256([]byte("ui.privatellms.msp/slug/v1:" + managedUID))
	return "ui-" + hex.EncodeToString(digest[:16])
}

func callbackFor(managedUID string) string {
	return "https://" + slug(managedUID) + "." + publicHost + CallbackPath
}

func entryFor(cluster, namespace, name, sourceUID, managedUID string) accesspolicy.Entry {
	return accesspolicy.Entry{
		UID: managedUID, Namespace: accesspolicy.RuntimeNamespace(cluster, namespace), Name: name,
		Source: accesspolicy.Source{Cluster: cluster, Namespace: namespace, Name: name, UID: sourceUID},
		Issuer: testIssuer, ClientID: "chat-ui", ClientSecretName: "chat-oauth",
	}
}

func (w *world) ListSources(_ context.Context, cluster string) ([]unstructured.Unstructured, error) {
	w.adapterCalls++
	w.listCalls++
	w.listed = append(w.listed, cluster)
	if w.onList != nil {
		w.onList(w, w.listCalls)
	}
	if w.listErr != nil {
		return nil, w.listErr
	}
	out := make([]unstructured.Unstructured, 0, len(w.sources[cluster]))
	for _, item := range w.sources[cluster] {
		out = append(out, *item.DeepCopy())
	}
	return out, nil
}

func (w *world) GetManaged(_ context.Context, namespace, name string) (*unstructured.Unstructured, error) {
	w.adapterCalls++
	if w.getErr != nil {
		return nil, w.getErr
	}
	object, ok := w.managed[namespace+"/"+name]
	if !ok {
		return nil, apierrors.NewNotFound(instances, name)
	}
	return object.DeepCopy(), nil
}

func (w *world) HasRuntime(_ context.Context, namespace, name string) (bool, error) {
	w.adapterCalls++
	w.runtimeReads++
	return w.runtimes[namespace+"/"+name], w.runtimeErr
}

func (w *world) GetEnrollment(context.Context) (*corev1.ConfigMap, error) {
	w.adapterCalls++
	if w.cm == nil {
		return nil, apierrors.NewNotFound(schema.GroupResource{Resource: "configmaps"}, "chat-enrollment")
	}
	return w.cm.DeepCopy(), nil
}

func (w *world) UpdateEnrollment(_ context.Context, original *corev1.ConfigMap, doc accesspolicy.EnrollmentDocument) error {
	w.adapterCalls++
	if original.ResourceVersion != w.cm.ResourceVersion || original.UID != w.cm.UID {
		return apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, w.cm.Name, errors.New("stale"))
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	w.cm.Data = map[string]string{accesspolicy.EnrollmentDataKey: string(data)}
	w.cm.ResourceVersion += "1"
	w.cmWrites++
	return nil
}

func (w *world) GetClient(context.Context, string) (*unstructured.Unstructured, error) {
	w.adapterCalls++
	return w.client.DeepCopy(), nil
}

func (w *world) UpdateRedirects(_ context.Context, original *unstructured.Unstructured, redirects, origins []string) error {
	w.adapterCalls++
	if original.GetUID() != w.client.GetUID() {
		return errors.New("stale client")
	}
	if err := unstructured.SetNestedStringSlice(w.client.Object, redirects, "spec", "forProvider", "validRedirectUris"); err != nil {
		return err
	}
	if err := unstructured.SetNestedStringSlice(w.client.Object, origins, "spec", "forProvider", "webOrigins"); err != nil {
		return err
	}
	w.clientWrites++
	w.redirects, w.origins = slices.Clone(redirects), slices.Clone(origins)
	return nil
}

func (w *world) Verify(_ context.Context, issuer, clientID, callback string) error {
	w.adapterCalls++
	if issuer != testIssuer || clientID != "chat-ui" {
		return errors.New("unexpected verifier arguments")
	}
	w.verified = append(w.verified, callback)
	if err := w.verifyFail[callback]; err != nil {
		return err
	}
	return w.verifyErr
}

func (w *world) doc(t *testing.T) accesspolicy.EnrollmentDocument {
	t.Helper()
	var doc accesspolicy.EnrollmentDocument
	if err := json.Unmarshal([]byte(w.cm.Data[accesspolicy.EnrollmentDataKey]), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func (w *world) registered(t *testing.T) []string {
	t.Helper()
	got, _, err := unstructured.NestedStringSlice(w.client.Object, "spec", "forProvider", "validRedirectUris")
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func mustReconcile(t *testing.T, e *Engine) Result {
	t.Helper()
	result, err := e.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	return result
}

func mustFail(t *testing.T, e *Engine) {
	t.Helper()
	if _, err := e.Reconcile(context.Background()); err == nil {
		t.Fatal("reconcile succeeded but must fail closed")
	}
}

func TestNewInstanceIsEnrolledWithExactRegistrations(t *testing.T) {
	w, e := newWorld(t)
	w.add("account-a", "default", "chat", "src-1", "msp-1")

	result := mustReconcile(t, e)
	want := entryFor("account-a", "default", "chat", "src-1", "msp-1")
	if !result.Changed || result.Entries != 1 || result.Pins != 1 || result.ClientsUpdated != 1 || result.Skipped != 0 {
		t.Fatalf("unexpected result %+v", result)
	}
	doc := w.doc(t)
	if len(doc.Entries) != 1 || doc.Entries[0] != want || len(doc.Pins) != 1 || doc.Pins[0] != want {
		t.Fatalf("unexpected document %+v", doc)
	}
	callback := callbackFor("msp-1")
	wantRedirects := []string{baseCallback, callback}
	slices.Sort(wantRedirects)
	wantOrigins := []string{baseOrigin, "https://" + slug("msp-1") + "." + publicHost}
	slices.Sort(wantOrigins)
	if !slices.Equal(w.redirects, wantRedirects) || !slices.Equal(w.origins, wantOrigins) {
		t.Fatalf("registrations are not exact: %v %v", w.redirects, w.origins)
	}
	if !slices.Equal(w.verified, []string{callback}) {
		t.Fatalf("callback was not verified before publication: %v", w.verified)
	}
	// The unchanged production gate selector accepts exactly this MSP object.
	object := w.managed[mspKey("account-a", "default", "chat")]
	got, err := e.Config.Policy.SelectEnrolled(object, strings.NewReader(w.cm.Data[accesspolicy.EnrollmentDataKey]))
	if err != nil || got != want {
		t.Fatalf("SelectEnrolled = %+v, %v", got, err)
	}
}

func TestUnchangedReconcileHasNoSideEffects(t *testing.T) {
	w, e := newWorld(t)
	w.add("account-a", "default", "chat", "src-1", "msp-1")
	mustReconcile(t, e)
	verified := len(w.verified)

	result := mustReconcile(t, e)
	if result.Changed || result.ClientsUpdated != 0 || result.Entries != 1 || result.Pins != 1 {
		t.Fatalf("unexpected result %+v", result)
	}
	if w.cmWrites != 1 || w.clientWrites != 1 || len(w.verified) != verified {
		t.Fatalf("recheck had side effects: map %d client %d verifications %d", w.cmWrites, w.clientWrites, len(w.verified)-verified)
	}
}

func TestDryRunInventoriesChangesWithoutPublishingOrProbing(t *testing.T) {
	w, e := newWorld(t)
	w.add("account-a", "default", "chat", "src-1", "msp-1")
	e.DryRun = true
	result := mustReconcile(t, e)
	if result.Entries != 1 || !result.MappingUpdatePlanned || result.ClientUpdatesPlanned != 1 {
		t.Fatalf("plan did not identify required changes: %+v", result)
	}
	if result.Changed || result.ClientsUpdated != 0 || w.cmWrites != 0 || w.clientWrites != 0 || len(w.verified) != 0 {
		t.Fatal("read-only plan performed registration, verification or publication")
	}
}

func TestConfiguredIssuerMustNameTheConfiguredRealm(t *testing.T) {
	w, e := newWorld(t)
	e.Config.Clients[0].Realm = "another-organization"
	mustFail(t, e)
	if w.adapterCalls != 0 {
		t.Fatal("realm/issuer mismatch reached an API")
	}
}

func TestForgedTenantMetadataCannotAlterIdentityOrRegistration(t *testing.T) {
	w, e := newWorld(t)
	w.add("account-a", "default", "chat", "src-1", "msp-1")
	forge := func(object *unstructured.Unstructured, annotations map[string]string) {
		object.SetLabels(map[string]string{"ui.privatellms.msp/slug": "victim", "kcp.io/cluster": "account-evil"})
		object.SetAnnotations(annotations)
		for _, path := range [][]string{{"spec", "slug"}, {"spec", "host"}} {
			_ = unstructured.SetNestedField(object.Object, "victim", path...)
		}
		_ = unstructured.SetNestedField(object.Object, "https://victim.chat.example.test", "status", "url")
	}
	forge(&w.sources["account-a"][0], map[string]string{ClusterAnnotation: "account-a", "ui.privatellms.msp/slug": "victim"})
	forge(w.managed[mspKey("account-a", "default", "chat")], map[string]string{"ui.privatellms.msp/slug": "victim"})

	mustReconcile(t, e)
	doc := w.doc(t)
	if len(doc.Entries) != 1 || doc.Entries[0] != entryFor("account-a", "default", "chat", "src-1", "msp-1") {
		t.Fatalf("forged metadata changed the entry: %+v", doc)
	}
	for _, value := range append(slices.Clone(w.redirects), w.origins...) {
		if strings.Contains(value, "victim") {
			t.Fatalf("forged slug was registered: %s", value)
		}
	}
	if !slices.Contains(w.redirects, callbackFor("msp-1")) {
		t.Fatalf("derived callback missing: %v", w.redirects)
	}
}

func TestOnlyApprovedWorkspacesAreListed(t *testing.T) {
	w, e := newWorld(t)
	w.add("account-a", "default", "chat", "src-1", "msp-1")
	w.add("account-evil", "default", "chat", "src-9", "msp-9")

	mustReconcile(t, e)
	for _, cluster := range w.listed {
		if cluster != "account-a" {
			t.Fatalf("unapproved workspace %q was listed", cluster)
		}
	}
	doc := w.doc(t)
	if len(doc.Entries) != 1 || doc.Entries[0].Source.Cluster != "account-a" || slices.Contains(w.redirects, callbackFor("msp-9")) {
		t.Fatalf("unapproved workspace was enrolled: %+v %v", doc, w.redirects)
	}
}

func TestImpossibleSourceInventoryBlocksEverything(t *testing.T) {
	cases := map[string]func(w *world){
		"duplicate uid": func(w *world) {
			w.add("account-a", "default", testOther, "src-1", "msp-2")
		},
		"duplicate coordinates": func(w *world) {
			w.sources["account-a"] = append(w.sources["account-a"], w.sources["account-a"][0])
		},
		"inconsistent managed identity": func(w *world) {
			w.managed[mspKey("account-a", "default", "chat")].SetName(testOther)
		},
		"more sources than the workspace capacity": func(w *world) {
			for i := 0; i <= maxSourcesPerWorkspace; i++ {
				w.sources["account-a"] = append(w.sources["account-a"], source("account-a", "default", fmt.Sprintf("n%d", i), fmt.Sprintf("s%d", i)))
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			w, e := newWorld(t)
			w.add("account-a", "default", "chat", "src-1", "msp-1")
			mutate(w)
			mustFail(t, e)
			if w.cmWrites != 0 || w.clientWrites != 0 {
				t.Fatalf("writes happened: map %d client %d", w.cmWrites, w.clientWrites)
			}
		})
	}
}

func TestInvalidSourceIsSkippedAndCountedWithoutBlockingOthers(t *testing.T) {
	set := func(path ...string) func(*unstructured.Unstructured, string) {
		return func(o *unstructured.Unstructured, v string) { _ = unstructured.SetNestedField(o.Object, v, path...) }
	}
	cases := map[string]func(s *unstructured.Unstructured){
		"cluster annotation of another workspace": func(s *unstructured.Unstructured) {
			s.SetAnnotations(map[string]string{ClusterAnnotation: "account-b"})
		},
		"missing cluster annotation": func(s *unstructured.Unstructured) { s.SetAnnotations(nil) },
		"wrong kind":                 func(s *unstructured.Unstructured) { s.SetKind("Other") },
		"wrong apiVersion":           func(s *unstructured.Unstructured) { s.SetAPIVersion("v1") },
		"empty uid":                  func(s *unstructured.Unstructured) { set("metadata", "uid")(s, "") },
		"invalid name":               func(s *unstructured.Unstructured) { set("metadata", "name")(s, "Bad_Name") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			w, e := newWorld(t)
			w.add("account-a", "default", "bad", "src-bad", "msp-bad")
			w.add("account-a", "default", "chat", "src-1", "msp-1")
			mutate(&w.sources["account-a"][0])

			result := mustReconcile(t, e)
			if result.Entries != 1 || result.Skipped != 1 || result.InvalidSources != 1 || !result.Changed {
				t.Fatalf("invalid source was not isolated: %+v", result)
			}
			if doc := w.doc(t); len(doc.Entries) != 1 || doc.Entries[0].Name != "chat" {
				t.Fatalf("unexpected document %+v", doc)
			}
		})
	}
}

func TestMissingManagedObjectIsCountedAsSkipped(t *testing.T) {
	w, e := newWorld(t)
	w.add("account-a", "default", "chat", "src-1", "msp-1")
	w.sources["account-a"] = append(w.sources["account-a"], source("account-a", "default", "lonely", "src-2"))
	result := mustReconcile(t, e)
	if result.Entries != 1 || result.Skipped != 1 || result.InvalidSources != 0 {
		t.Fatalf("unexpected result %+v", result)
	}
}

func TestRecreatedSourceCannotRebindSurvivingManagedObject(t *testing.T) {
	w, e := newWorld(t)
	w.add("account-a", "default", "chat", "src-1", "msp-1")
	mustReconcile(t, e)
	old := entryFor("account-a", "default", "chat", "src-1", "msp-1")

	// The source disappears; the MSP object survives. The pin must persist.
	w.sources["account-a"] = nil
	result := mustReconcile(t, e)
	if result.Entries != 0 || result.Pins != 1 || w.doc(t).Pins[0] != old {
		t.Fatalf("pin was not preserved across source absence: %+v %+v", result, w.doc(t))
	}
	// Registration is additive: the retired callback stays; authorization is decided by the document.
	retired := []string{baseCallback, callbackFor("msp-1")}
	slices.Sort(retired)
	if !slices.Equal(w.registered(t), retired) {
		t.Fatalf("registered callbacks were removed or changed: %v", w.registered(t))
	}

	// A new source with the same name and a new UID must not bind to the survivor.
	w.sources["account-a"] = []unstructured.Unstructured{source("account-a", "default", "chat", "src-2")}
	writes, verified := w.cmWrites, len(w.verified)
	result = mustReconcile(t, e)
	doc := w.doc(t)
	if result.Entries != 0 || result.Skipped != 1 || result.Changed || w.cmWrites != writes || len(w.verified) != verified {
		t.Fatalf("recreated source was bound: %+v", result)
	}
	if len(doc.Entries) != 0 || len(doc.Pins) != 1 || doc.Pins[0] != old {
		t.Fatalf("unexpected document %+v", doc)
	}
	if !slices.Equal(w.registered(t), retired) || w.clientWrites != 1 {
		t.Fatalf("a refused binding changed the registrations: %v (writes %d)", w.registered(t), w.clientWrites)
	}
}

func TestFreshUIDIsAllowedOnceOldManagedObjectIsGone(t *testing.T) {
	for name, replace := range map[string]func(t *testing.T, w *world, e *Engine){
		"managed object not found first": func(t *testing.T, w *world, e *Engine) {
			delete(w.managed, mspKey("account-a", "default", "chat"))
			if got := mustReconcile(t, e).Pins; got != 0 {
				t.Fatalf("pin of a vanished object survived: %d", got)
			}
			w.add("account-a", "default", "chat", "src-2", "msp-2")
		},
		"managed object replaced in place": func(_ *testing.T, w *world, _ *Engine) {
			w.add("account-a", "default", "chat", "src-2", "msp-2")
		},
	} {
		t.Run(name, func(t *testing.T) {
			w, e := newWorld(t)
			w.add("account-a", "default", "chat", "src-1", "msp-1")
			mustReconcile(t, e)
			w.sources["account-a"] = nil
			mustReconcile(t, e)

			replace(t, w, e)
			result := mustReconcile(t, e)
			want := entryFor("account-a", "default", "chat", "src-2", "msp-2")
			doc := w.doc(t)
			if result.Entries != 1 || result.Pins != 1 || len(doc.Entries) != 1 || doc.Entries[0] != want || doc.Pins[0] != want {
				t.Fatalf("fresh UID was not enrolled exactly: %+v %+v", result, doc)
			}
		})
	}
}

func TestDeletingObjectsAreNotEnrolled(t *testing.T) {
	for name, mutate := range map[string]func(w *world){
		"source": func(w *world) {
			_ = unstructured.SetNestedField(w.sources["account-a"][0].Object, "2026-02-01T00:00:00Z", "metadata", "deletionTimestamp")
		},
		"managed object": func(w *world) {
			_ = unstructured.SetNestedField(w.managed[mspKey("account-a", "default", "chat")].Object, "2026-02-01T00:00:00Z", "metadata", "deletionTimestamp")
		},
	} {
		t.Run(name, func(t *testing.T) {
			w, e := newWorld(t)
			w.add("account-a", "default", "chat", "src-1", "msp-1")
			mutate(w)
			if result := mustReconcile(t, e); result.Entries != 0 {
				t.Fatalf("deleting %s was enrolled", name)
			}
			if len(w.redirects) != 0 || len(w.verified) != 0 {
				t.Fatalf("deleting %s was registered", name)
			}
		})
	}
}

func TestUnavailableReadsNeverWrite(t *testing.T) {
	for name, fail := range map[string]func(w *world){
		"source list":   func(w *world) { w.listErr = errors.New("kcp unavailable") },
		"managed read":  func(w *world) { w.getErr = errors.New("api server unavailable") },
		"malformed MSP": func(w *world) { w.managed[mspKey("account-a", "default", "new")].SetKind("Other") },
	} {
		t.Run(name, func(t *testing.T) {
			w, e := newWorld(t)
			w.add("account-a", "default", "chat", "src-1", "msp-1")
			mustReconcile(t, e)
			cmWrites, clientWrites := w.cmWrites, w.clientWrites

			// A new instance would normally cause both a client and a map write.
			w.add("account-a", "default", "new", "src-2", "msp-2")
			fail(w)
			mustFail(t, e)
			if w.cmWrites != cmWrites || w.clientWrites != clientWrites {
				t.Fatalf("failure caused writes: map %d client %d", w.cmWrites-cmWrites, w.clientWrites-clientWrites)
			}
			if len(w.doc(t).Entries) != 1 {
				t.Fatal("failed read was treated as an empty inventory")
			}
		})
	}
}

func TestClientMismatchBlocksAllWrites(t *testing.T) {
	forProvider := func(field string, value any) func(c *unstructured.Unstructured) {
		return func(c *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(c.Object, value, "spec", "forProvider", field)
		}
	}
	remove := func(field string) func(c *unstructured.Unstructured) {
		return func(c *unstructured.Unstructured) {
			unstructured.RemoveNestedField(c.Object, "spec", "forProvider", field)
		}
	}
	cases := map[string]func(c *unstructured.Unstructured){
		"uid": func(c *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(c.Object, testOther, "metadata", "uid")
		},
		"provider": func(c *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(c.Object, testOther, "spec", "providerConfigRef", "name")
		},
		"no provider": func(c *unstructured.Unstructured) {
			unstructured.RemoveNestedField(c.Object, "spec", "providerConfigRef")
		},
		"realm":        forProvider("realmId", testOther),
		"client id":    forProvider("clientId", testOther),
		"not opted in": func(c *unstructured.Unstructured) { c.SetAnnotations(nil) },
		"opt-in false": func(c *unstructured.Unstructured) {
			c.SetAnnotations(map[string]string{OwnedCallbacksAnnotation: "false"})
		},
		"deleting": func(c *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(c.Object, "2026-02-01T00:00:00Z", "metadata", "deletionTimestamp")
		},
		"wrong kind":    func(c *unstructured.Unstructured) { c.SetKind("Role") },
		"public client": forProvider("accessType", "PUBLIC"),
		"no access":     remove("accessType"),
		"plain pkce":    forProvider("pkceCodeChallengeMethod", "plain"),
		"no pkce":       remove("pkceCodeChallengeMethod"),
		"disabled":      forProvider("enabled", false),
		"enabled unset": remove("enabled"),
		"no std flow":   forProvider("standardFlowEnabled", false),
		"std unset":     remove("standardFlowEnabled"),
		"direct grants": forProvider("directAccessGrantsEnabled", true),
		"implicit":      forProvider("implicitFlowEnabled", true),
		"service acct":  forProvider("serviceAccountsEnabled", true),
		"exchange":      forProvider("standardTokenExchangeEnabled", true),
		"bad bool type": forProvider("implicitFlowEnabled", "false"),
		"bad redirects": forProvider("validRedirectUris", "not-a-list"),
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			w, e := newWorld(t)
			w.add("account-a", "default", "chat", "src-1", "msp-1")
			mutate(w.client)
			mustFail(t, e)
			if w.cmWrites != 0 || w.clientWrites != 0 {
				t.Fatalf("writes happened: map %d client %d", w.cmWrites, w.clientWrites)
			}
		})
	}
}

func TestMissingOptionalFalseSettingsAreAccepted(t *testing.T) {
	w, e := newWorld(t)
	w.add("account-a", "default", "chat", "src-1", "msp-1")
	unstructured.RemoveNestedField(w.client.Object, "spec", "forProvider", "directAccessGrantsEnabled")
	if result := mustReconcile(t, e); result.Entries != 1 {
		t.Fatalf("unexpected result %+v", result)
	}
}

// managedFields builds one managedFields item whose fieldsV1 is the given value.
func managedFields(manager string, fieldsV1 any) []any {
	item := map[string]any{"manager": manager, "operation": "Apply"}
	if fieldsV1 != nil {
		item["fieldsV1"] = fieldsV1
	}
	return []any{item}
}

func forProviderFields(field string) map[string]any {
	return map[string]any{"f:spec": map[string]any{"f:forProvider": map[string]any{field: map[string]any{}}}}
}

func TestAnyOtherManagerOwningCallbackFieldsBlocksWithoutForcing(t *testing.T) {
	statusOnly := map[string]any{"f:status": map[string]any{"f:atProvider": map[string]any{"f:id": map[string]any{}}}}
	cases := []struct {
		name    string
		managed any // value of metadata.managedFields; nil means absent
		blocked bool
	}{
		{"flux owns redirects", managedFields("kustomize-controller", forProviderFields("f:validRedirectUris")), true},
		{"flux owns origins", managedFields("kustomize-controller", forProviderFields("f:webOrigins")), true},
		{"helm owns redirects", managedFields("helm-controller", forProviderFields("f:validRedirectUris")), true},
		{"helm client-side owner", managedFields("helm", forProviderFields("f:webOrigins")), true},
		{"argo owns redirects", managedFields("argocd-controller", forProviderFields("f:validRedirectUris")), true},
		{"kubectl owns origins", managedFields("kubectl-client-side-apply", forProviderFields("f:webOrigins")), true},
		{"crossplane provider owns redirects", managedFields("crossplane-provider", forProviderFields("f:validRedirectUris")), true},
		{"unknown manager owns redirects", managedFields("somebody-else", forProviderFields("f:validRedirectUris")), true},
		{"similar manager name is not the enroller", managedFields("chat-ui-enrollment-2", forProviderFields("f:validRedirectUris")), true},
		{"fieldsV1 as JSON string", managedFields("helm", `{"f:spec":{"f:forProvider":{"f:validRedirectUris":{}}}}`), true},
		{"second item owns", append(managedFields("provider", statusOnly), managedFields("helm", forProviderFields("f:webOrigins"))...), true},

		{"own manager owns callbacks", managedFields("chat-ui-enrollment", forProviderFields("f:validRedirectUris")), false},
		{"foreign status-only ownership", managedFields("crossplane-provider", statusOnly), false},
		{"foreign owns unrelated spec field", managedFields("kustomize-controller", forProviderFields("f:clientId")), false},
		{"foreign item without fields", managedFields("kubectl-client-side-apply", nil), false},
		{"no managedFields", nil, false},
		{"empty managedFields", []any{}, false},

		{"managedFields is not a list", "oops", true},
		{"item is not an object", []any{"oops"}, true},
		{"item without manager", []any{map[string]any{"fieldsV1": forProviderFields("f:validRedirectUris")}}, true},
		{"manager is not a string", []any{map[string]any{"manager": int64(1)}}, true},
		{"fieldsV1 is a number", managedFields("provider", int64(1)), true},
		{"fieldsV1 string is not JSON", managedFields("provider", "{"), true},
		{"f:spec is not an object", managedFields("provider", map[string]any{"f:spec": "x"}), true},
		{"f:forProvider is not an object", managedFields("provider", map[string]any{"f:spec": map[string]any{"f:forProvider": []any{}}}), true},
		{"own manager with malformed fields", managedFields("chat-ui-enrollment", map[string]any{"f:spec": int64(7)}), true},
		{"foreign atomic spec owner", managedFields("helm", map[string]any{"f:spec": map[string]any{}}), true},
		{"foreign atomic provider owner", managedFields("helm", map[string]any{"f:spec": map[string]any{"f:forProvider": map[string]any{}}}), true},
		{"foreign self-only provider owner", managedFields("helm", forProviderFields(".")), true},
		{"own manager malformed callback leaf", managedFields("chat-ui-enrollment", map[string]any{"f:spec": map[string]any{"f:forProvider": map[string]any{"f:validRedirectUris": int64(7)}}}), true},
		{"own manager malformed callback set", managedFields("chat-ui-enrollment", map[string]any{"f:spec": map[string]any{"f:forProvider": map[string]any{"f:webOrigins": map[string]any{"v:origin": true}}}}), true},
		{"foreign parent presence with unrelated field", managedFields("helm", map[string]any{"f:spec": map[string]any{".": map[string]any{}, "f:forProvider": map[string]any{".": map[string]any{}, "f:clientId": map[string]any{}}}}), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, e := newWorld(t)
			w.add("account-a", "default", "chat", "src-1", "msp-1")
			if tc.managed != nil {
				if err := unstructured.SetNestedField(w.client.Object, tc.managed, "metadata", "managedFields"); err != nil {
					t.Fatal(err)
				}
			}
			_, err := e.Reconcile(context.Background())
			if tc.blocked {
				if err == nil || w.cmWrites != 0 || w.clientWrites != 0 {
					t.Fatalf("foreign ownership was not respected: %v map %d client %d", err, w.cmWrites, w.clientWrites)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUnreadyCallbackPublishesNoEntryUntilAcceptedButPinsIt(t *testing.T) {
	w, e := newWorld(t)
	w.add("account-a", "default", "chat", "src-1", "msp-1")
	w.verifyErr = errors.New("redirect_uri not registered yet")

	result := mustReconcile(t, e)
	want := entryFor("account-a", "default", "chat", "src-1", "msp-1")
	if result.Pending != 1 || result.Probed != 1 || result.Entries != 0 || result.Pins != 1 || !result.Changed {
		t.Fatalf("unexpected result %+v", result)
	}
	if doc := w.doc(t); len(doc.Entries) != 0 || len(doc.Pins) != 1 || doc.Pins[0] != want {
		t.Fatalf("entry was published before its callback was accepted, or the pin is missing: %+v", doc)
	}
	if w.clientWrites != 1 {
		t.Fatalf("registration should have been requested once, got %d", w.clientWrites)
	}

	w.verifyErr = nil
	result = mustReconcile(t, e)
	if !result.Changed || result.Entries != 1 || result.Pending != 0 || w.clientWrites != 1 {
		t.Fatalf("unexpected result %+v (client writes %d)", result, w.clientWrites)
	}
}

func TestPendingCallbackDoesNotBlockOtherEntriesOrRemovals(t *testing.T) {
	w, e := newWorld(t)
	w.add("account-a", "default", "old", "src-o", "msp-o")
	mustReconcile(t, e)

	// A new unready instance sorts before a new ready one; the old one is removed.
	w.add("account-a", "default", "aaa-slow", "src-a", "msp-a")
	w.add("account-a", "default", "zzz-fast", "src-z", "msp-z")
	w.sources["account-a"] = w.sources["account-a"][1:]
	w.verifyFail = map[string]error{callbackFor("msp-a"): errors.New("not accepted yet")}

	result := mustReconcile(t, e)
	doc := w.doc(t)
	if result.Entries != 1 || result.Pending != 1 || result.Pins != 3 || !result.Changed {
		t.Fatalf("unexpected result %+v", result)
	}
	if len(doc.Entries) != 1 || doc.Entries[0] != entryFor("account-a", "default", "zzz-fast", "src-z", "msp-z") {
		t.Fatalf("good entry was blocked or the removal was not applied: %+v", doc.Entries)
	}
	// Pins of the removed (MSP object survives) and of the pending instance are kept.
	names := []string{}
	for _, pin := range doc.Pins {
		names = append(names, pin.Name)
	}
	if !slices.Equal(names, []string{"aaa-slow", "old", "zzz-fast"}) {
		t.Fatalf("pins were lost: %v", names)
	}
	if !slices.Contains(w.registered(t), callbackFor("msp-a")) {
		t.Fatalf("pending callback was not registered: %v", w.registered(t))
	}

	// Retry: still pending, nothing changes; then it becomes ready and is published.
	writes := w.cmWrites
	result = mustReconcile(t, e)
	if result.Changed || result.Pending != 1 || w.cmWrites != writes {
		t.Fatalf("pending retry wrote or lost state: %+v", result)
	}
	delete(w.verifyFail, callbackFor("msp-a"))
	result = mustReconcile(t, e)
	if !result.Changed || result.Entries != 2 || result.Pending != 0 {
		t.Fatalf("pending entry was not published once accepted: %+v", result)
	}
}

func TestRevocationWorksWhileAnotherCallbackFails(t *testing.T) {
	w, e := newWorld(t)
	w.add("account-a", "default", "gone", "src-g", "msp-g")
	w.add("account-a", "default", "kept", "src-k", "msp-k")
	mustReconcile(t, e)

	w.sources["account-a"] = w.sources["account-a"][1:] // "gone" disappears
	w.add("account-a", "default", "slow", "src-s", "msp-s")
	w.verifyErr = errors.New("unavailable")
	result := mustReconcile(t, e)
	doc := w.doc(t)
	if !result.Changed || result.Pending != 1 || len(doc.Entries) != 1 || doc.Entries[0].Name != "kept" {
		t.Fatalf("revocation was blocked by a failing callback: %+v %+v", result, doc)
	}
}

func TestPendingCandidateKeepsPinSoRecreatedSourceCannotRebind(t *testing.T) {
	w, e := newWorld(t)
	w.add("account-a", "default", "chat", "src-1", "msp-1")
	w.verifyErr = errors.New("not yet")
	mustReconcile(t, e)
	pin := entryFor("account-a", "default", "chat", "src-1", "msp-1")

	// Between passes the source is recreated with a new UID while the MSP object survives.
	w.verifyErr = nil
	w.sources["account-a"] = []unstructured.Unstructured{source("account-a", "default", "chat", "src-2")}
	result := mustReconcile(t, e)
	if result.Entries != 0 || result.Skipped != 1 || result.Probed != 0 {
		t.Fatalf("recreated source was bound or probed: %+v", result)
	}
	if doc := w.doc(t); len(doc.Entries) != 0 || len(doc.Pins) != 1 || doc.Pins[0] != pin {
		t.Fatalf("pin was lost: %+v", doc)
	}
}

func TestProbesAreBoundedAndOffsetMakesProgress(t *testing.T) {
	const total = 12
	w, e := newWorld(t)
	for i := range total {
		w.add("account-a", "default", fmt.Sprintf("i%02d", i), fmt.Sprintf("s%02d", i), fmt.Sprintf("m%02d", i))
	}
	// The first nine never become ready, so a fixed window of eight would starve the rest.
	w.verifyFail = map[string]error{}
	for i := range 9 {
		w.verifyFail[callbackFor(fmt.Sprintf("m%02d", i))] = errors.New("not ready")
	}

	if ProbeBatchSize != 8 {
		t.Fatalf("ProbeBatchSize = %d", ProbeBatchSize)
	}
	dry := *e
	dry.DryRun = true
	plan := mustReconcile(t, &dry)
	if plan.ProbesPlanned != ProbeBatchSize || plan.Pending != total-ProbeBatchSize || plan.Entries != ProbeBatchSize || plan.Pins != total ||
		!plan.MappingUpdatePlanned || len(w.verified) != 0 || w.cmWrites != 0 {
		t.Fatalf("dry-run plan is not the bounded subset: %+v", plan)
	}

	published := 0
	for pass := range 2 * total {
		before := len(w.verified)
		e.ProbeOffset = pass
		result := mustReconcile(t, e)
		if probes := len(w.verified) - before; probes > ProbeBatchSize || result.Probed != probes {
			t.Fatalf("pass %d probed %d times: %+v", pass, probes, result)
		}
		if result.Pins != total {
			t.Fatalf("pass %d: pins were lost: %+v", pass, result)
		}
		published = result.Entries
		if published == 3 {
			break
		}
	}
	if published != 3 {
		t.Fatalf("offset rotation never reached the ready candidates: %d published", published)
	}
	if got := len(w.doc(t).Entries); got != 3 {
		t.Fatalf("stored entries = %d", got)
	}
}

func TestPublishedEntriesAreNotReprobedWhileOthersPend(t *testing.T) {
	w, e := newWorld(t)
	w.add("account-a", "default", "chat", "src-1", "msp-1")
	mustReconcile(t, e)
	w.add("account-a", "default", "slow", "src-2", "msp-2")
	w.verifyErr = errors.New("not yet")
	before := len(w.verified)
	result := mustReconcile(t, e)
	if got := w.verified[before:]; !slices.Equal(got, []string{callbackFor("msp-2")}) || result.Entries != 1 || result.Pending != 1 {
		t.Fatalf("published entry was probed again or lost: %v %+v", got, result)
	}
}

func TestChangedPolicyConfigurationForSameBindingIsReplacedAfterFreshVerification(t *testing.T) {
	w, e := newWorld(t)
	w.add("account-a", "default", "chat", "src-1", "msp-1")
	mustReconcile(t, e)
	old := entryFor("account-a", "default", "chat", "src-1", "msp-1")

	e.Config.Policy.Enrollment.Workspaces[0].ClientSecretName = "chat-oauth-2"
	want := old
	want.ClientSecretName = "chat-oauth-2"

	// While the callback is not accepted the previous mapping stays exactly as it was.
	w.verifyErr = errors.New("not yet")
	result := mustReconcile(t, e)
	if doc := w.doc(t); result.Pending != 1 || len(doc.Entries) != 1 || doc.Entries[0] != old || len(doc.Pins) != 1 || doc.Pins[0] != old {
		t.Fatalf("previous mapping was altered before verification: %+v %+v", result, doc)
	}

	w.verifyErr = nil
	verified := len(w.verified)
	result = mustReconcile(t, e)
	doc := w.doc(t)
	if !result.Changed || len(w.verified)-verified != 1 || len(doc.Entries) != 1 || doc.Entries[0] != want || len(doc.Pins) != 1 || doc.Pins[0] != want {
		t.Fatalf("configuration change was not applied exactly once: %+v %+v", result, doc)
	}
	object := w.managed[mspKey("account-a", "default", "chat")]
	if got, err := e.Config.Policy.SelectEnrolled(object, strings.NewReader(w.cm.Data[accesspolicy.EnrollmentDataKey])); err != nil || got != want {
		t.Fatalf("SelectEnrolled = %+v, %v", got, err)
	}

	// A different source UID for the same MSP object stays blocked.
	w.sources["account-a"] = []unstructured.Unstructured{source("account-a", "default", "chat", "src-2")}
	result = mustReconcile(t, e)
	if result.Entries != 0 || result.Skipped != 1 || len(w.doc(t).Pins) != 1 || w.doc(t).Pins[0] != want {
		t.Fatalf("source UID change was not blocked: %+v", result)
	}
}

func TestStaleManagedObjectIsNotBoundToRecreatedSource(t *testing.T) {
	w, e := newWorld(t)
	// The MSP object predates its source: a stale runtime that a recreated source must not inherit.
	w.add("account-a", "default", "chat", "src-new", "msp-old")
	_ = unstructured.SetNestedField(w.managed[mspKey("account-a", "default", "chat")].Object, "2025-06-01T00:00:00Z", "metadata", "creationTimestamp")
	w.add("account-a", "default", "independent", "src-i", "msp-i")

	result := mustReconcile(t, e)
	doc := w.doc(t)
	if result.StaleBindings != 1 || result.Skipped != 1 || result.Entries != 1 || result.Pins != 1 ||
		len(doc.Entries) != 1 || doc.Entries[0].Name != "independent" || len(doc.Pins) != 1 {
		t.Fatalf("stale binding was not isolated: %+v %+v", result, doc)
	}
	if slices.Contains(w.registered(t), callbackFor("msp-old")) || slices.Contains(w.verified, callbackFor("msp-old")) {
		t.Fatal("callback of a refused binding was registered or probed")
	}
}

func TestFirstBindingNeedsSourceCreationTimeButPinnedBindingDoesNot(t *testing.T) {
	t.Run("zero source timestamp", func(t *testing.T) {
		w, e := newWorld(t)
		w.add("account-a", "default", "chat", "src-1", "msp-1")
		unstructured.RemoveNestedField(w.sources["account-a"][0].Object, "metadata", "creationTimestamp")
		result := mustReconcile(t, e)
		if result.Entries != 0 || result.StaleBindings != 1 || result.Skipped != 1 || result.Pins != 0 {
			t.Fatalf("unproven first binding was accepted: %+v", result)
		}
	})
	t.Run("equal timestamps are accepted", func(t *testing.T) {
		w, e := newWorld(t)
		w.add("account-a", "default", "chat", "src-1", "msp-1")
		_ = unstructured.SetNestedField(w.sources["account-a"][0].Object, "2026-01-01T00:00:00Z", "metadata", "creationTimestamp")
		if result := mustReconcile(t, e); result.Entries != 1 || result.StaleBindings != 0 {
			t.Fatalf("unexpected result %+v", result)
		}
	})
	t.Run("pinned binding survives a later timestamp change", func(t *testing.T) {
		w, e := newWorld(t)
		w.add("account-a", "default", "chat", "src-1", "msp-1")
		mustReconcile(t, e)
		unstructured.RemoveNestedField(w.sources["account-a"][0].Object, "metadata", "creationTimestamp")
		if result := mustReconcile(t, e); result.Entries != 1 || result.StaleBindings != 0 {
			t.Fatalf("pinned binding was re-judged by timestamps: %+v", result)
		}
	})
}

func TestRegistrationIsAdditiveAndRefusesForeignValues(t *testing.T) {
	retired := "https://" + slug("msp-retired") + "." + publicHost + CallbackPath
	retiredOrigin := "https://" + slug("msp-retired") + "." + publicHost
	set := func(c *unstructured.Unstructured, field string, values []string) {
		if err := unstructured.SetNestedStringSlice(c.Object, values, "spec", "forProvider", field); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("existing safe callbacks and origins are preserved", func(t *testing.T) {
		w, e := newWorld(t)
		w.add("account-a", "default", "chat", "src-1", "msp-1")
		set(w.client, "validRedirectUris", []string{retired, baseCallback})
		set(w.client, "webOrigins", []string{retiredOrigin, baseOrigin})

		dry := *e
		dry.DryRun = true
		if plan := mustReconcile(t, &dry); plan.CallbackAdditionsPlanned != 1 || plan.ClientUpdatesPlanned != 1 || w.clientWrites != 0 {
			t.Fatalf("planned additions are not reported: %+v", plan)
		}

		result := mustReconcile(t, e)
		if result.CallbackAdditionsPlanned != 1 || result.ClientsUpdated != 1 {
			t.Fatalf("unexpected result %+v", result)
		}
		wantRedirects := []string{baseCallback, callbackFor("msp-1"), retired}
		wantOrigins := []string{baseOrigin, "https://" + slug("msp-1") + "." + publicHost, retiredOrigin}
		slices.Sort(wantRedirects)
		slices.Sort(wantOrigins)
		if !slices.Equal(w.redirects, wantRedirects) || !slices.Equal(w.origins, wantOrigins) {
			t.Fatalf("registrations are not the exact union: %v %v", w.redirects, w.origins)
		}

		// Nothing to add: no further write even though the stored order is arbitrary.
		set(w.client, "validRedirectUris", slices.Concat([]string{retired}, wantRedirects))
		before := w.clientWrites
		if result := mustReconcile(t, e); result.CallbackAdditionsPlanned != 0 || w.clientWrites != before {
			t.Fatalf("no-op registration wrote: %+v", result)
		}
	})

	for name, tc := range map[string]struct {
		field  string
		values []string
	}{
		"foreign host redirect":   {"validRedirectUris", []string{baseCallback, "https://app.example.org/oauth2/callback"}},
		"http redirect":           {"validRedirectUris", []string{baseCallback, "http://x.chat.example.test/oauth2/callback"}},
		"wildcard redirect":       {"validRedirectUris", []string{baseCallback, "https://*.chat.example.test/oauth2/callback"}},
		"redirect with port":      {"validRedirectUris", []string{baseCallback, "https://x.chat.example.test:8443/oauth2/callback"}},
		"redirect other path":     {"validRedirectUris", []string{baseCallback, "https://x.chat.example.test/other"}},
		"redirect nested label":   {"validRedirectUris", []string{baseCallback, "https://a.b.chat.example.test/oauth2/callback"}},
		"empty redirect":          {"validRedirectUris", []string{baseCallback, ""}},
		"foreign origin":          {"webOrigins", []string{baseOrigin, "https://evil.example.org"}},
		"wildcard origin":         {"webOrigins", []string{baseOrigin, "*"}},
		"origin with path":        {"webOrigins", []string{baseOrigin, "https://x.chat.example.test/"}},
		"http origin":             {"webOrigins", []string{baseOrigin, "http://x.chat.example.test"}},
		"origin of the apex host": {"webOrigins", []string{baseOrigin, "https://" + publicHost}},
	} {
		t.Run("refuses "+name, func(t *testing.T) {
			w, e := newWorld(t)
			w.add("account-a", "default", "chat", "src-1", "msp-1")
			set(w.client, tc.field, tc.values)
			mustFail(t, e)
			if w.cmWrites != 0 || w.clientWrites != 0 || len(w.verified) != 0 {
				t.Fatalf("writes or probes happened: map %d client %d probes %d", w.cmWrites, w.clientWrites, len(w.verified))
			}
		})
	}
}

func TestIdentityRaceDuringRegistrationBlocksPublication(t *testing.T) {
	key := mspKey("account-a", "default", "chat")
	cases := map[string]func(w *world){
		"source recreated": func(w *world) {
			_ = unstructured.SetNestedField(w.sources["account-a"][0].Object, "src-race", "metadata", "uid")
		},
		"managed object recreated": func(w *world) { w.managed[key] = managedObject("account-a", "default", "chat", "msp-race") },
		"source deleted":           func(w *world) { w.sources["account-a"] = nil },
	}
	for name, race := range cases {
		t.Run(name, func(t *testing.T) {
			w, e := newWorld(t)
			w.add("account-a", "default", "chat", "src-1", "msp-1")
			w.onList = func(w *world, call int) {
				if call == 2 { // the second inventory, after registration and verification
					race(w)
				}
			}
			mustFail(t, e)
			if w.cmWrites != 0 {
				t.Fatal("stale identity was published")
			}
		})
	}
}

func TestStaticMappingsAndDefaultsAreUnaffected(t *testing.T) {
	static := entryFor("account-a", "default", "legacy", "src-s", "msp-s")

	t.Run("exact duplicate of a static entry is permitted", func(t *testing.T) {
		w, e := newWorld(t)
		e.Config.Policy.Entries = []accesspolicy.Entry{static}
		w.add("account-a", "default", "legacy", "src-s", "msp-s")
		if result := mustReconcile(t, e); result.Entries != 1 || result.Skipped != 0 {
			t.Fatalf("unexpected result %+v", result)
		}
	})

	for name, setup := range map[string]func(w *world){
		"different source UID": func(w *world) { w.add("account-a", "default", "legacy", "src-x", "msp-s") },
		"different MSP UID":    func(w *world) { w.add("account-a", "default", "legacy", "src-s", "msp-x") },
	} {
		t.Run("static disagreement: "+name, func(t *testing.T) {
			w, e := newWorld(t)
			e.Config.Policy.Entries = []accesspolicy.Entry{static}
			setup(w)
			w.add("account-a", "default", "chat", "src-1", "msp-1")
			result := mustReconcile(t, e)
			if result.Entries != 1 || result.Skipped != 1 || w.doc(t).Entries[0].Name != "chat" {
				t.Fatalf("static disagreement was not isolated: %+v %+v", result, w.doc(t))
			}
			if len(e.Config.Policy.Entries) != 1 || e.Config.Policy.Entries[0] != static {
				t.Fatal("static policy changed")
			}
		})
	}

	t.Run("pre-cutover unmapped instance is not invented a hostname", func(t *testing.T) {
		w, e := newWorld(t)
		w.add("account-a", "default", "old", "src-o", "msp-o")
		_ = unstructured.SetNestedField(w.managed[mspKey("account-a", "default", "old")].Object, "2019-01-01T00:00:00Z", "metadata", "creationTimestamp")
		_ = unstructured.SetNestedField(w.sources["account-a"][0].Object, "2018-12-31T00:00:00Z", "metadata", "creationTimestamp")
		w.add("account-a", "default", "chat", "src-1", "msp-1")
		result := mustReconcile(t, e)
		if result.Entries != 1 || result.Skipped != 1 || slices.Contains(w.redirects, callbackFor("msp-o")) {
			t.Fatalf("unexpected result %+v", result)
		}
	})

	t.Run("enrollment disabled by default touches nothing", func(t *testing.T) {
		w, e := newWorld(t)
		e.Config.Policy.Enrollment = nil
		mustFail(t, e)
		if w.adapterCalls != 0 {
			t.Fatalf("disabled enrollment called %d adapters", w.adapterCalls)
		}
	})
}

func TestConfigurationGraphIsValidatedBeforeAnyAdapterCall(t *testing.T) {
	baseURI := func(uri string) func(e *Engine) {
		return func(e *Engine) { e.Config.Clients[0].BaseRedirectURIs = []string{uri} }
	}
	clone := func(change func(c *ClientConfig)) func(e *Engine) {
		return func(e *Engine) {
			c := e.Config.Clients[0]
			c.BaseRedirectURIs = nil
			change(&c)
			e.Config.Clients = append(e.Config.Clients, c)
		}
	}
	cases := map[string]func(e *Engine){
		"workspace without client":  func(e *Engine) { e.Config.Clients = nil },
		"duplicate name":            clone(func(c *ClientConfig) { c.UID, c.ClientID = "u2", testOther }),
		"duplicate uid":             clone(func(c *ClientConfig) { c.Name, c.ClientID = testOther, testOther }),
		"duplicate issuer/client":   clone(func(c *ClientConfig) { c.Name, c.UID = testOther, "u2" }),
		"unused client":             clone(func(c *ClientConfig) { c.Name, c.UID, c.ClientID = testOther, "u2", testOther }),
		"missing uid":               func(e *Engine) { e.Config.Clients[0].UID = "" },
		"missing realm":             func(e *Engine) { e.Config.Clients[0].Realm = "" },
		"missing provider config":   func(e *Engine) { e.Config.Clients[0].ProviderConfigName = "" },
		"plain http issuer":         func(e *Engine) { e.Config.Clients[0].Issuer = "http://id.example.test/realms/showroom" },
		"empty public host":         func(e *Engine) { e.Config.PublicHost = "" },
		"public host is a URL":      func(e *Engine) { e.Config.PublicHost = "https://" + publicHost },
		"public host with space":    func(e *Engine) { e.Config.PublicHost = " " + publicHost },
		"nil hostname policy":       func(e *Engine) { e.Config.Hostnames = nil },
		"wildcard base":             baseURI("https://*.chat.example.test/oauth2/callback"),
		"http base":                 baseURI("http://admin.chat.example.test/oauth2/callback"),
		"query in base":             baseURI(baseCallback + "?x=1"),
		"fragment in base":          baseURI(baseCallback + "#x"),
		"trailing slash":            baseURI(baseCallback + "/"),
		"other path":                baseURI("https://admin.chat.example.test/callback"),
		"foreign host":              baseURI("https://admin.example.org/oauth2/callback"),
		"nested label":              baseURI("https://a.b.chat.example.test/oauth2/callback"),
		"apex host":                 baseURI("https://chat.example.test/oauth2/callback"),
		"port":                      baseURI("https://admin.chat.example.test:8443/oauth2/callback"),
		"userinfo":                  baseURI((&url.URL{Scheme: "https", Host: "admin." + publicHost, Path: CallbackPath, User: url.User("synthetic-user")}).String()),
		"suffix lookalike":          baseURI("https://admin.evilchat.example.test/oauth2/callback"),
		"uppercase":                 baseURI("https://ADMIN.chat.example.test/oauth2/callback"),
		"whitespace":                baseURI("https://admin.chat.example.test/oauth2/callback "),
		"workspace issuer mismatch": func(e *Engine) { e.Config.Policy.Enrollment.Workspaces[0].ClientID = "unconfigured" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			w, e := newWorld(t)
			w.add("account-a", "default", "chat", "src-1", "msp-1")
			mutate(e)
			mustFail(t, e)
			if w.adapterCalls != 0 {
				t.Fatalf("invalid configuration reached %d adapter calls", w.adapterCalls)
			}
		})
	}
}

func TestEnrollmentMapRequirements(t *testing.T) {
	cases := map[string]func(w *world){
		"absent":              func(w *world) { w.cm = nil },
		"missing label":       func(w *world) { w.cm.Labels = nil },
		"foreign label value": func(w *world) { w.cm.Labels[ManagedByLabel] = "helm" },
		"no UID":              func(w *world) { w.cm.UID = "" },
		"no resourceVersion":  func(w *world) { w.cm.ResourceVersion = "" },
		"other name":          func(w *world) { w.cm.Name = testOther },
		"other namespace":     func(w *world) { w.cm.Namespace = "elsewhere" },
		"deleting": func(w *world) {
			now := metav1.Now()
			w.cm.DeletionTimestamp = &now
		},
		"missing key": func(w *world) { w.cm.Data = map[string]string{} },
		"bad JSON":    func(w *world) { w.cm.Data[accesspolicy.EnrollmentDataKey] = "{" },
		"unknown field": func(w *world) {
			w.cm.Data[accesspolicy.EnrollmentDataKey] = `{"version":1,"entries":[],"pins":[],"x":1}`
		},
		"wrong version": func(w *world) { w.cm.Data[accesspolicy.EnrollmentDataKey] = `{"version":2,"entries":[],"pins":[]}` },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			w, e := newWorld(t)
			w.add("account-a", "default", "chat", "src-1", "msp-1")
			mutate(w)
			mustFail(t, e)
			if w.cmWrites != 0 || w.clientWrites != 0 || w.listCalls != 0 {
				t.Fatalf("unexpected activity: map %d client %d lists %d", w.cmWrites, w.clientWrites, w.listCalls)
			}
		})
	}
}

// Relies on DecodeEnrollment validating structure and exact pins, not the current allowlist.
func TestPinsOfRemovedWorkspacesSurviveWhileTheManagedObjectExists(t *testing.T) {
	w, e := newWorld(t)
	old := entryFor("account-old", "default", "n", "s-old", "m-old") // workspace is not (or no longer) approved
	data, err := json.Marshal(accesspolicy.EnrollmentDocument{Version: 1, Entries: []accesspolicy.Entry{}, Pins: []accesspolicy.Entry{old}})
	if err != nil {
		t.Fatal(err)
	}
	w.cm = enrollmentMap(string(data))
	w.managed[mspKey("account-old", "default", "n")] = managedObject("account-old", "default", "n", "m-old")

	if result := mustReconcile(t, e); result.Pins != 1 || result.Changed {
		t.Fatalf("historical pin was not preserved: %+v", result)
	}
	delete(w.managed, mspKey("account-old", "default", "n"))
	if result := mustReconcile(t, e); result.Pins != 0 || !result.Changed {
		t.Fatalf("pin of a vanished MSP object survived: %+v", result)
	}
}

func TestBootstrapDocumentIsValidAndStoreConflictIsReported(t *testing.T) {
	w, e := newWorld(t)
	if result := mustReconcile(t, e); result.Changed || result.Entries != 0 || w.cmWrites != 0 || w.clientWrites != 0 {
		t.Fatalf("empty bootstrap must be a no-op: %+v", result)
	}

	// An optimistic-concurrency failure from the Store surfaces as an error.
	w.add("account-a", "default", "chat", "src-1", "msp-1")
	w.onList = func(w *world, call int) {
		// Call 1 was the bootstrap run; call 2 is the first inventory of this run,
		// which happens after the map was read.
		if call == 2 {
			w.cm.ResourceVersion = "8" // someone else updated the map in between
		}
	}
	mustFail(t, e)
	if w.cmWrites != 0 {
		t.Fatal("write succeeded despite a stale resourceVersion")
	}
}

func TestSameNameInDifferentWorkspacesAndNamespacesKeepsDistinctBindings(t *testing.T) {
	w, e := newWorld(t)
	second := e.Config.Policy.Enrollment.Workspaces[0]
	second.Cluster = "account-b"
	e.Config.Policy.Enrollment.Workspaces = append(e.Config.Policy.Enrollment.Workspaces, second)
	for i, coords := range [][2]string{{"account-a", "default"}, {"account-a", "another"}, {"account-b", "default"}} {
		w.add(coords[0], coords[1], "chat", fmt.Sprintf("source-%d", i), fmt.Sprintf("runtime-%d", i))
	}
	if result := mustReconcile(t, e); result.Entries != 3 {
		t.Fatalf("independent instances were conflated: %+v", result)
	}
	expected := entryFor("account-b", "default", "chat", "source-2", "runtime-2")
	if !slices.Contains(w.doc(t).Entries, expected) {
		t.Fatal("binding points to the wrong workspace or runtime")
	}
	otherNamespace := entryFor("account-a", "another", "chat", "source-1", "runtime-1")
	if !slices.Contains(w.doc(t).Entries, otherNamespace) {
		t.Fatal("binding points to the wrong namespace")
	}
}

func TestFirstBindingRefusesPriorIdentityOrRuntimeState(t *testing.T) {
	cases := map[string]func(*world){
		"existing runtime": func(w *world) { w.runtimes = map[string]bool{mspKey("account-a", "default", "chat"): true} },
		"access identity": func(w *world) {
			_ = unstructured.SetNestedStringMap(w.managed[mspKey("account-a", "default", "chat")].Object,
				map[string]string{"chatui-session-old": "secret-uid"}, "status", "accessSecretUIDs")
		},
		"malformed identity map": func(w *world) {
			_ = unstructured.SetNestedField(w.managed[mspKey("account-a", "default", "chat")].Object,
				"invalid", "status", "accessSecretUIDs")
		},
		"malformed identity value": func(w *world) {
			_ = unstructured.SetNestedField(w.managed[mspKey("account-a", "default", "chat")].Object,
				map[string]any{"key": true}, "status", "accessSecretUIDs")
		},
		"malformed status": func(w *world) { w.managed[mspKey("account-a", "default", "chat")].Object["status"] = true },
	}
	for name, prior := range cases {
		t.Run(name, func(t *testing.T) {
			w, e := newWorld(t)
			w.add("account-a", "default", "chat", "replacement-source", "surviving-runtime")
			// Creation timestamps have only second precision. Equal times cannot
			// establish that this runtime belongs to the replacement source.
			w.sources["account-a"][0].SetCreationTimestamp(w.managed[mspKey("account-a", "default", "chat")].GetCreationTimestamp())
			prior(w)
			w.add("account-a", "default", "independent", "source-good", "runtime-good")
			result := mustReconcile(t, e)
			if result.Entries != 1 || result.Pins != 1 || result.StaleBindings != 1 || w.doc(t).Entries[0].Name != "independent" {
				t.Fatalf("first binding adopted prior state or blocked an independent instance: %+v", result)
			}
			if slices.Contains(w.verified, callbackFor("surviving-runtime")) || slices.Contains(w.registered(t), callbackFor("surviving-runtime")) {
				t.Fatal("refused runtime was registered or probed")
			}
		})
	}
}

func TestPinnedAndReviewedBindingsDoNotReadPriorRuntime(t *testing.T) {
	w, e := newWorld(t)
	w.add("account-a", "default", "chat", "source-1", "runtime-1")
	mustReconcile(t, e)
	reads := w.runtimeReads
	w.runtimeErr = errors.New("runtime lookup must not be used for an established binding")
	_ = unstructured.SetNestedStringMap(w.managed[mspKey("account-a", "default", "chat")].Object,
		map[string]string{"session": "uid"}, "status", "accessSecretUIDs")
	if result := mustReconcile(t, e); result.Entries != 1 || w.runtimeReads != reads {
		t.Fatal("existing pinned binding depended on runtime adoption checks")
	}
	// An exact operator-reviewed mapping can explicitly recover a lost document.
	w.cm = enrollmentMap(emptyDoc)
	e.Config.Policy.Entries = []accesspolicy.Entry{entryFor("account-a", "default", "chat", "source-1", "runtime-1")}
	if result := mustReconcile(t, e); result.Entries != 1 || w.runtimeReads != reads {
		t.Fatal("exact reviewed recovery was not respected")
	}
}

func TestLostEnrollmentDocumentDoesNotReadoptRunningInstance(t *testing.T) {
	w, e := newWorld(t)
	w.add("account-a", "default", "chat", "source-1", "runtime-1")
	mustReconcile(t, e)
	w.cm = enrollmentMap(emptyDoc)
	w.runtimes = map[string]bool{mspKey("account-a", "default", "chat"): true}
	if result := mustReconcile(t, e); result.Entries != 0 || result.Pins != 0 || result.StaleBindings != 1 {
		t.Fatalf("lost history silently readopted a used runtime: %+v", result)
	}
}

func TestFirstBindingRechecksRuntimeAfterCallbackProbe(t *testing.T) {
	for _, state := range []string{"runtime", "identity", "API failure"} {
		t.Run(state, func(t *testing.T) {
			w, e := newWorld(t)
			w.add("account-a", "default", "chat", "source-1", "runtime-1")
			w.onList = func(w *world, call int) {
				if call != 2 {
					return
				}
				switch state {
				case "runtime":
					w.runtimes = map[string]bool{mspKey("account-a", "default", "chat"): true}
				case "identity":
					_ = unstructured.SetNestedStringMap(w.managed[mspKey("account-a", "default", "chat")].Object,
						map[string]string{"session": "uid"}, "status", "accessSecretUIDs")
				case "API failure":
					w.runtimeErr = errors.New("read timed out")
				}
			}
			mustFail(t, e)
			if len(w.verified) != 1 || w.cmWrites != 0 {
				t.Fatal("state appearing during registration was published")
			}
		})
	}
}

func TestRuntimeReadFailureIsNotAbsence(t *testing.T) {
	w, e := newWorld(t)
	w.add("account-a", "default", "chat", "source-1", "runtime-1")
	w.runtimeErr = errors.New("runtime API denied")
	mustFail(t, e)
	if w.cmWrites != 0 || w.clientWrites != 0 || len(w.verified) != 0 {
		t.Fatal("unknown runtime state was treated as empty")
	}
}

func TestRetainedCallbackCapacityFailsBeforeAnyWrite(t *testing.T) {
	for _, retained := range []int{maxRegisteredCallbacks - 1, maxRegisteredCallbacks} {
		t.Run(fmt.Sprint(retained), func(t *testing.T) {
			w, e := newWorld(t)
			callbacks, origins := []string{baseCallback}, []string{baseOrigin}
			for i := 1; i < retained; i++ {
				origin := fmt.Sprintf("https://retired-%d.%s", i, publicHost)
				callbacks, origins = append(callbacks, origin+CallbackPath), append(origins, origin)
			}
			if err := unstructured.SetNestedStringSlice(w.client.Object, callbacks, "spec", "forProvider", "validRedirectUris"); err != nil {
				t.Fatal(err)
			}
			if err := unstructured.SetNestedStringSlice(w.client.Object, origins, "spec", "forProvider", "webOrigins"); err != nil {
				t.Fatal(err)
			}
			w.add("account-a", "default", "chat", "new-source", "new-runtime")
			result, err := e.Reconcile(context.Background())
			if retained == maxRegisteredCallbacks {
				if err == nil || w.clientWrites != 0 || w.cmWrites != 0 || len(w.verified) != 0 {
					t.Fatal("callback limit did not reject before changes")
				}
			} else if err != nil || result.Entries != 1 || len(w.redirects) != maxRegisteredCallbacks {
				t.Fatalf("valid capacity boundary rejected: %+v %v", result, err)
			}
		})
	}
}
