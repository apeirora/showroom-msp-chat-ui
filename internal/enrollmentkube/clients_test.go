package enrollmentkube

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/apeirora/showroom-msp-chat-ui/internal/accesspolicy"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
)

func tlsConfig(server *httptest.Server) *rest.Config {
	return &rest.Config{Host: server.URL, TLSClientConfig: rest.TLSClientConfig{
		CAData: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}),
	}}
}

func TestSourcePaginationIsCompleteOrFails(t *testing.T) {
	failSecond := false
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodGet || r.URL.Path != "/clusters/approved/apis/ui.privatellms.msp/v1alpha1/chatuiinstances" || r.URL.Query().Get("limit") != "200" {
			t.Error("source read escaped its configured workspace or resource")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("continue") == "next" {
			if failSecond {
				http.Error(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"ServiceUnavailable","code":503}`, http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte(`{"apiVersion":"ui.privatellms.msp/v1alpha1","kind":"ChatUIInstanceList","metadata":{},"items":[{"metadata":{"name":"second"}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"apiVersion":"ui.privatellms.msp/v1alpha1","kind":"ChatUIInstanceList","metadata":{"continue":"next"},"items":[{"metadata":{"name":"first"}}]}`))
	}))
	defer server.Close()
	sources := Sources{Config: tlsConfig(server)}
	items, err := sources.ListSources(context.Background(), "approved")
	if err != nil || len(items) != 2 || calls != 2 {
		t.Fatalf("incomplete inventory: %d, %v", len(items), err)
	}
	failSecond = true
	items, err = sources.ListSources(context.Background(), "approved")
	if err == nil || items != nil {
		t.Fatal("failed page was treated as a complete inventory")
	}
}

func TestAPIRedirectIsNotFollowed(t *testing.T) {
	redirected := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/foreign" {
			redirected = true
			w.WriteHeader(200)
			return
		}
		http.Redirect(w, r, "/foreign", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	_, err := (Sources{Config: tlsConfig(server)}).ListSources(context.Background(), "approved")
	if err == nil || redirected {
		t.Fatal("API redirect was followed or accepted")
	}
}

func TestWritesUseExactFieldsAndObjectPreconditions(t *testing.T) {
	var paths []string
	var patches [][]map[string]any
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Error("unexpected write method")
		}
		paths = append(paths, r.URL.Path)
		var patch []map[string]any
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			t.Error(err)
		}
		patches = append(patches, patch)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Status","status":"Success"}`))
	}))
	defer server.Close()
	api, err := NewDynamic(tlsConfig(server))
	if err != nil {
		t.Fatal(err)
	}
	store := Store{API: api, Namespace: "operator", Name: "enrollment"}
	config := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "operator", Name: "enrollment", UID: "map-uid", ResourceVersion: "42"}}
	if err := store.UpdateEnrollment(context.Background(), config, accesspolicy.EnrollmentDocument{Version: 1}); err != nil {
		t.Fatal(err)
	}
	client := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "openidclient.keycloak.crossplane.io/v1alpha1", "kind": "Client", "metadata": map[string]any{"name": "reviewed", "uid": "client-uid", "resourceVersion": "43"}}}
	if err := (Registry{API: api}).UpdateRedirects(context.Background(), client, []string{"https://owned.example.test/oauth2/callback"}, []string{"https://owned.example.test"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(paths, []string{"/api/v1/namespaces/operator/configmaps/enrollment", "/apis/openidclient.keycloak.crossplane.io/v1alpha1/clients/reviewed"}) {
		t.Fatal("write escaped named targets")
	}
	wantFields := [][]string{{"/metadata/uid", "/metadata/resourceVersion", "/data/enrollment.json"}, {"/metadata/uid", "/metadata/resourceVersion", "/spec/forProvider/validRedirectUris", "/spec/forProvider/webOrigins"}}
	for i, patch := range patches {
		if len(patch) != len(wantFields[i]) {
			t.Fatal("unexpected patch field")
		}
		for j, operation := range patch {
			if operation["path"] != wantFields[i][j] || j < 2 && operation["op"] != "test" {
				t.Fatal("missing object precondition or wrong field")
			}
		}
	}
}

func TestCredentialSourceRejectsPluginsAndWrongTargets(t *testing.T) {
	file := filepath.Join(t.TempDir(), "kubeconfig")
	write := func(server, auth, tls string) {
		data := "apiVersion: v1\nkind: Config\ncurrent-context: selected\ncontexts:\n- name: selected\n  context: {cluster: selected, user: selected}\nclusters:\n- name: selected\n  cluster:\n    server: " + server + "\n" + tls + "users:\n- name: selected\n  user:\n" + auth
		if err := os.WriteFile(file, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("https://api.example.test/clusters/provider", "    token: synthetic-only\n", "")
	if _, err := Config(file, "https://api.example.test", false); err != nil {
		t.Fatal(err)
	}
	if _, err := Config(file, "https://other.example.test", false); err == nil {
		t.Fatal("wrong API origin accepted")
	}
	write("https://api.example.test", "    token: synthetic-only\n", "    insecure-skip-tls-verify: true\n")
	if _, err := Config(file, "https://api.example.test", false); err == nil {
		t.Fatal("unverified TLS accepted")
	}
	write("https://api.example.test", "    exec:\n      apiVersion: client.authentication.k8s.io/v1\n      command: must-never-execute\n      interactiveMode: Never\n", "")
	if _, err := Config(file, "https://api.example.test", false); err == nil {
		t.Fatal("exec authentication accepted")
	}
	for _, auth := range []string{"    username: synthetic\n    password: synthetic\n", "    token: synthetic\n    as: admin\n",
		"    token: synthetic\n    as-groups: [system:masters]\n"} {
		write("https://api.example.test", auth, "")
		if _, err := Config(file, "https://api.example.test", false); err == nil {
			t.Fatal("basic authentication or impersonation accepted")
		}
	}
}

func TestPriorRuntimeLookupRequiresAuthoritativeAbsence(t *testing.T) {
	for _, scenario := range []struct {
		name                  string
		deployment, ingress   int
		wantExists, wantError bool
		wantCalls             int
	}{
		{"neither exists", 404, 404, false, false, 2},
		{"deployment exists", 200, 404, true, false, 1},
		{"ingress exists", 404, 200, true, false, 2},
		{"deployment read denied", 403, 404, false, true, 1},
		{"ingress read denied", 404, 403, false, true, 2},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var calls []string
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.URL.Path)
				status := scenario.deployment
				switch r.URL.Path {
				case "/apis/apps/v1/namespaces/runtime/deployments/example-chatui":
				case "/apis/networking.k8s.io/v1/namespaces/runtime/ingresses/example-chatui":
					status = scenario.ingress
				default:
					t.Error("lookup escaped exact runtime resource names")
				}
				if r.Method != http.MethodGet {
					t.Error("runtime lookup mutated state")
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				switch status {
				case http.StatusOK:
					_, _ = w.Write([]byte(`{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"example-chatui","namespace":"runtime"}}`))
				case http.StatusNotFound:
					_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Status","status":"Failure","reason":"NotFound","code":404}`))
				default:
					_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Status","status":"Failure","reason":"Forbidden","code":403}`))
				}
			}))
			defer server.Close()
			api, err := NewDynamic(tlsConfig(server))
			if err != nil {
				t.Fatal(err)
			}
			exists, err := (Managed{API: api}).HasRuntime(context.Background(), "runtime", "example")
			if exists != scenario.wantExists || (err != nil) != scenario.wantError || len(calls) != scenario.wantCalls {
				t.Fatalf("wrong existence result %v, error %v, calls %v", exists, err, calls)
			}
		})
	}
}
