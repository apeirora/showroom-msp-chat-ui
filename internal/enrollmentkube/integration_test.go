package enrollmentkube

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/apeirora/showroom-msp-chat-ui/internal/accesspolicy"
	"github.com/apeirora/showroom-msp-chat-ui/internal/enrollment"
	"github.com/apeirora/showroom-msp-chat-ui/internal/hostnamepolicy"
	jsonpatch "github.com/evanphx/json-patch/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	integrationSourcePath = "/clusters/account-a/apis/ui.privatellms.msp/v1alpha1/chatuiinstances"
	integrationMapPath    = "/api/v1/namespaces/chat-system/configmaps/chat-enrollment"
	integrationClientPath = "/apis/openidclient.keycloak.crossplane.io/v1alpha1/clients/chat-ui"
)

// integrationAPI is an HTTP API fixture, not an adapter fake: the real Kubernetes
// adapters must send narrow JSON patches whose UID/RV tests actually pass. It
// deliberately implements no Secret endpoints or arbitrary mutation methods.
type integrationAPI struct {
	t       *testing.T
	mu      sync.Mutex
	objects map[string]map[string]any
	writes  int
}

func (api *integrationAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	api.mu.Lock()
	defer api.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	object, exists := api.objects[r.URL.Path]
	if !exists {
		api.t.Errorf("unexpected API target %s %s", r.Method, r.URL.Path)
		http.Error(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"NotFound","code":404}`, 404)
		return
	}
	if object == nil {
		http.Error(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"NotFound","code":404}`, http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if err := json.NewEncoder(w).Encode(object); err != nil {
			api.t.Error(err)
		}
	case http.MethodPatch:
		api.patch(w, r, object)
	default:
		api.t.Errorf("unexpected API method %s", r.Method)
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (api *integrationAPI) patch(w http.ResponseWriter, r *http.Request, object map[string]any) {
	allowed := map[string][]string{
		integrationMapPath:    {"/data/enrollment.json"},
		integrationClientPath: {"/spec/forProvider/validRedirectUris", "/spec/forProvider/webOrigins"},
	}
	fields, ok := allowed[r.URL.Path]
	if !ok || r.Header.Get("Content-Type") != "application/json-patch+json" || r.URL.Query().Get("fieldManager") != "chat-ui-enrollment" {
		api.t.Error("write escaped target, JSONPatch type or field manager")
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		api.t.Error(err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var operations []struct{ Op, Path string }
	if err := json.Unmarshal(body, &operations); err != nil || len(operations) != len(fields)+2 {
		api.t.Error("patch has unexpected operation count")
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	for i, operation := range operations {
		if i < 2 {
			if operation.Op != "test" || operation.Path != []string{"/metadata/uid", "/metadata/resourceVersion"}[i] {
				api.t.Error("write omitted UID/RV preconditions")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		} else if !slices.Contains(fields, operation.Path) || operation.Op != "add" && operation.Op != "replace" {
			api.t.Error("write escaped allowed fields")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	}
	patch, err := jsonpatch.DecodePatch(body)
	if err != nil {
		api.t.Error(err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	raw, err := json.Marshal(object)
	if err != nil {
		api.t.Error(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	updated, err := patch.Apply(raw)
	if err != nil {
		http.Error(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Invalid","code":422}`, http.StatusUnprocessableEntity)
		return
	}
	var next map[string]any
	if err := json.Unmarshal(updated, &next); err != nil {
		api.t.Error(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	metadata := next["metadata"].(map[string]any)
	version, err := strconv.Atoi(metadata["resourceVersion"].(string))
	if err != nil {
		api.t.Error(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	metadata["resourceVersion"] = strconv.Itoa(version + 1)
	api.objects[r.URL.Path] = next
	api.writes++
	if err := json.NewEncoder(w).Encode(next); err != nil {
		api.t.Error(err)
	}
}

func (api *integrationAPI) writeCount() int {
	api.mu.Lock()
	defer api.mu.Unlock()
	return api.writes
}

type integrationOIDC struct {
	t          *testing.T
	mu         sync.Mutex
	callback   string
	propagated bool
	probes     int
}

func (oidc *integrationOIDC) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	oidc.mu.Lock()
	defer oidc.mu.Unlock()
	oidc.probes++
	query := r.URL.Query()
	if r.Method != http.MethodGet || r.URL.Path != "/realms/showroom/protocol/openid-connect/auth" ||
		query.Get("client_id") != "chat-ui" || query.Get("redirect_uri") != oidc.callback || query.Get("prompt") != "none" ||
		query.Get("response_type") != "code" || query.Get("code_challenge_method") != "S256" ||
		query.Get("state") == "" || query.Get("nonce") == "" || query.Get("code_challenge") == "" ||
		r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
		oidc.t.Error("unexpected callback probe or credential transmission")
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if !oidc.propagated {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, oidc.callback+"?"+url.Values{"error": {"login_required"}, "state": {query.Get("state")}}.Encode(), http.StatusFound)
}

func (oidc *integrationOIDC) state(accept bool) int {
	oidc.mu.Lock()
	defer oidc.mu.Unlock()
	oidc.propagated = accept
	return oidc.probes
}

func integrationInstance(namespace, uid, created string) map[string]any {
	return map[string]any{
		"apiVersion": enrollment.InstanceAPIVersion, "kind": enrollment.InstanceKind,
		"metadata": map[string]any{"name": "example", "namespace": namespace, "uid": uid, "creationTimestamp": created,
			"annotations": map[string]any{enrollment.ClusterAnnotation: "account-a"}},
	}
}

func integrationClient() map[string]any {
	return map[string]any{
		"apiVersion": enrollment.ClientAPIVersion, "kind": enrollment.ClientKind,
		"metadata": map[string]any{"name": "chat-ui", "uid": "client-uid", "resourceVersion": "1",
			"annotations": map[string]any{enrollment.OwnedCallbacksAnnotation: "true"}},
		"spec": map[string]any{"providerConfigRef": map[string]any{"name": "keycloak"},
			"forProvider": map[string]any{"realmId": "showroom", "clientId": "chat-ui", "accessType": "CONFIDENTIAL",
				"enabled": true, "standardFlowEnabled": true, "directAccessGrantsEnabled": false,
				"pkceCodeChallengeMethod": "S256", "validRedirectUris": []any{}, "webOrigins": []any{},
				"name": "Preserve unrelated client settings"}},
	}
}

func TestEnrollmentAcrossTLSAPIsWaitsForCallbackAndPublishesExactIdentity(t *testing.T) {
	namespace := accesspolicy.RuntimeNamespace("account-a", "default")
	digest := sha256.Sum256([]byte("ui.privatellms.msp/slug/v1:managed-uid"))
	callback := "https://ui-" + hex.EncodeToString(digest[:16]) + ".chat.example.test/oauth2/callback"
	oidc := &integrationOIDC{t: t, callback: callback}
	identity := httptest.NewTLSServer(oidc)
	defer identity.Close()
	issuer := identity.URL + "/realms/showroom"
	roots := x509.NewCertPool()
	roots.AddCert(identity.Certificate())
	source := integrationInstance("default", "source-uid", "2026-01-01T00:00:00Z")
	kcpFixture := &integrationAPI{t: t, objects: map[string]map[string]any{integrationSourcePath: {
		"apiVersion": enrollment.InstanceAPIVersion, "kind": "ChatUIInstanceList", "metadata": map[string]any{}, "items": []any{source},
	}}}
	kcp := httptest.NewTLSServer(kcpFixture)
	defer kcp.Close()
	managedPath := "/apis/ui.privatellms.msp/v1alpha1/namespaces/" + namespace + "/chatuiinstances/example"
	mspFixture := &integrationAPI{t: t, objects: map[string]map[string]any{
		managedPath: integrationInstance(namespace, "managed-uid", "2026-01-01T00:00:01Z"),
		"/apis/apps/v1/namespaces/" + namespace + "/deployments/example-chatui":            nil,
		"/apis/networking.k8s.io/v1/namespaces/" + namespace + "/ingresses/example-chatui": nil,
		integrationMapPath: {
			"apiVersion": "v1", "kind": "ConfigMap",
			"metadata": map[string]any{"name": "chat-enrollment", "namespace": "chat-system", "uid": "map-uid", "resourceVersion": "1",
				"labels": map[string]any{enrollment.ManagedByLabel: enrollment.ManagedByValue}},
			"data": map[string]any{accesspolicy.EnrollmentDataKey: `{"version":1,"entries":[],"pins":[]}`},
		},
	}}
	msp := httptest.NewTLSServer(mspFixture)
	defer msp.Close()
	platformFixture := &integrationAPI{t: t, objects: map[string]map[string]any{integrationClientPath: integrationClient()}}
	platform := httptest.NewTLSServer(platformFixture)
	defer platform.Close()
	mspAPI, err := NewDynamic(tlsConfig(msp))
	if err != nil {
		t.Fatal(err)
	}
	platformAPI, err := NewDynamic(tlsConfig(platform))
	if err != nil {
		t.Fatal(err)
	}
	policy := &accesspolicy.Policy{KCPServer: kcp.URL, CredentialsNamespace: "chat-system",
		Enrollment: &accesspolicy.Enrollment{ConfigMapName: "chat-enrollment", Workspaces: []accesspolicy.Workspace{{
			Cluster: "account-a", Issuer: issuer, ClientID: "chat-ui", ClientSecretName: "chat-oauth"}}}}
	hostnames, err := hostnamepolicy.Parse(strings.NewReader(`{"cutover":"2020-01-01T00:00:00Z","entries":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	store := Store{API: mspAPI, Namespace: "chat-system", Name: "chat-enrollment"}
	registry := Registry{API: platformAPI}
	engine := enrollment.Engine{
		Config: enrollment.Config{Policy: policy, Hostnames: hostnames, PublicHost: "chat.example.test",
			Clients: []enrollment.ClientConfig{{Name: "chat-ui", UID: "client-uid", Realm: "showroom", Issuer: issuer,
				ClientID: "chat-ui", ProviderConfigName: "keycloak"}}},
		Sources: Sources{Config: tlsConfig(kcp)}, Managed: Managed{API: mspAPI}, Store: store, Registry: registry,
		Verifier: enrollment.NewHTTPVerifier(roots, time.Second), DryRun: true,
	}
	ctx := context.Background()
	if _, err := engine.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if platformFixture.writeCount() != 0 || mspFixture.writeCount() != 0 || oidc.state(false) != 0 {
		t.Fatal("dry run wrote to an API or probed the issuer")
	}
	engine.DryRun = false
	if _, err := engine.Reconcile(ctx); err != nil {
		t.Fatalf("pending registration should preserve progress: %v", err)
	}

	doc, _ := readIntegrationDocument(t, ctx, store, policy)
	if len(doc.Entries) != 0 || len(doc.Pins) != 1 || platformFixture.writeCount() != 1 || oidc.state(true) != 1 {
		t.Fatal("unpropagated callback was published, not registered, or lost its identity pin")
	}
	if _, err := engine.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	doc, raw := readIntegrationDocument(t, ctx, store, policy)
	want := accesspolicy.Entry{UID: "managed-uid", Namespace: namespace, Name: "example",
		Source: accesspolicy.Source{Cluster: "account-a", Namespace: "default", Name: "example", UID: "source-uid"},
		Issuer: issuer, ClientID: "chat-ui", ClientSecretName: "chat-oauth"}
	if len(doc.Entries) != 1 || doc.Entries[0] != want || len(doc.Pins) != 1 || doc.Pins[0] != want {
		t.Fatalf("wrong published binding: %+v", doc)
	}
	object := &metav1.ObjectMeta{UID: "managed-uid", Namespace: namespace, Name: "example"}
	if selected, err := policy.SelectEnrolled(object, strings.NewReader(raw)); err != nil || selected != want {
		t.Fatalf("verified mapping was not consumable: %v", err)
	}
	object.UID = "replacement-uid"
	if _, err := policy.SelectEnrolled(object, strings.NewReader(raw)); !errors.Is(err, accesspolicy.ErrUnmapped) {
		t.Fatal("a recreated runtime inherited the old mapping")
	}
	writes, probes := mspFixture.writeCount(), oidc.state(true)
	if _, err := engine.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if mspFixture.writeCount() != writes || platformFixture.writeCount() != 1 || oidc.state(true) != probes {
		t.Fatal("unchanged reconciliation performed new writes or probes")
	}
	checkClientPatchPreconditions(t, ctx, registry, callback, platformFixture)

	// A newly created source must not inherit the surviving old runtime, even
	// after the first registration succeeded and the callback is still valid.
	kcpFixture.mu.Lock()
	source["metadata"].(map[string]any)["uid"] = "replacement-source"
	source["metadata"].(map[string]any)["creationTimestamp"] = "2026-01-01T00:01:00Z"
	kcpFixture.mu.Unlock()
	if _, err := engine.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	doc, _ = readIntegrationDocument(t, ctx, store, policy)
	if len(doc.Entries) != 0 || len(doc.Pins) != 1 || doc.Pins[0].Source.UID != "source-uid" {
		t.Fatal("source recreation inherited surviving runtime or discarded its source pin")
	}
}

func checkClientPatchPreconditions(t *testing.T, ctx context.Context, registry Registry, callback string, platformFixture *integrationAPI) {
	t.Helper()
	registered, err := registry.GetClient(ctx, "chat-ui")
	if err != nil {
		t.Fatal(err)
	}
	if name, _, _ := unstructured.NestedString(registered.Object, "spec", "forProvider", "name"); name != "Preserve unrelated client settings" {
		t.Fatal("registration changed an unrelated client field")
	}
	registered.SetResourceVersion("0")
	if err := registry.UpdateRedirects(ctx, registered, []string{callback}, []string{"https://invalid.example.test"}); err == nil {
		t.Fatal("stale resourceVersion patch was accepted by the API fixture")
	}
	if platformFixture.writeCount() != 1 {
		t.Fatal("failed precondition changed the client")
	}
}

func readIntegrationDocument(t *testing.T, ctx context.Context, store Store, policy *accesspolicy.Policy) (accesspolicy.EnrollmentDocument, string) {
	t.Helper()
	config, err := store.GetEnrollment(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw := config.Data[accesspolicy.EnrollmentDataKey]
	doc, err := policy.DecodeEnrollment(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return *doc, raw
}
