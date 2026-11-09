// Package enrollmentkube implements the narrow API operations used by enrollment.
// It never reads application/client Secrets or writes to KCP.
package enrollmentkube

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/apeirora/showroom-msp-chat-ui/internal/accesspolicy"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	chatResource       = schema.GroupVersionResource{Group: "ui.privatellms.msp", Version: "v1alpha1", Resource: "chatuiinstances"}
	clientResource     = schema.GroupVersionResource{Group: "openidclient.keycloak.crossplane.io", Version: "v1alpha1", Resource: "clients"}
	configMapResource  = schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	namespaceResource  = schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
	deploymentResource = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	ingressResource    = schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}
)

// Config loads only the explicitly supplied credential source. API origins are
// pinned independently, TLS verification is mandatory and exec/auth plugins are
// not invoked. Call again on each pass to observe mounted credential rotation.
func Config(path, expectedOrigin string, inCluster bool) (*rest.Config, error) {
	if accesspolicy.HTTPSURL(expectedOrigin, true) != nil || (inCluster && path != "") || (!inCluster && path == "") {
		return nil, errors.New("explicit credential source and HTTPS API origin required")
	}
	var config *rest.Config
	var err error
	if inCluster {
		config, err = rest.InClusterConfig()
	} else {
		config, err = clientcmd.BuildConfigFromFlags("", path)
	}
	if err != nil {
		return nil, errors.New("enrollment kubeconfig unavailable")
	}
	expected, _ := url.Parse(expectedOrigin)
	if config.Insecure || config.ExecProvider != nil || config.AuthProvider != nil || config.Username != "" || config.Password != "" ||
		config.Impersonate.UserName != "" || config.Impersonate.UID != "" || len(config.Impersonate.Groups) != 0 || len(config.Impersonate.Extra) != 0 ||
		(config.ServerName != "" && config.ServerName != expected.Hostname()) ||
		!sameOrigin(config.Host, expectedOrigin) {
		return nil, errors.New("enrollment kubeconfig target or authentication mode rejected")
	}
	config = rest.CopyConfig(config)
	config.Host = expectedOrigin
	config.Timeout = 30 * time.Second
	config.UserAgent = "chat-ui-enrollment"
	config.Proxy = func(*http.Request) (*url.URL, error) { return nil, nil }
	config.QPS, config.Burst = 5, 10
	return config, nil
}

// NewDynamic keeps redirects from forwarding a service credential to a different
// API route or server. The selected API itself is the only permitted destination.
func NewDynamic(config *rest.Config) (dynamic.Interface, error) {
	client, err := rest.HTTPClientFor(config)
	if err != nil {
		return nil, errors.New("enrollment API transport unavailable")
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return dynamic.NewForConfigAndClient(config, client)
}

func sameOrigin(a, b string) bool {
	parse := func(raw string) (string, bool) {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return "", false
		}
		port := u.Port()
		if port == "" {
			port = "443"
		}
		return strings.ToLower(u.Hostname()) + ":" + port, true
	}
	x, validA := parse(a)
	y, validB := parse(b)
	return validA && validB && x == y
}

func VerifyCluster(ctx context.Context, api dynamic.Interface, expectedUID string) error {
	if expectedUID == "" {
		return errors.New("cluster UID is required")
	}
	ns, err := api.Resource(namespaceResource).Get(ctx, "kube-system", metav1.GetOptions{})
	if err != nil || string(ns.GetUID()) != expectedUID || ns.GetDeletionTimestamp() != nil {
		return errors.New("enrollment cluster identity verification failed")
	}
	return nil
}

type Sources struct{ Config *rest.Config }

func (s Sources) ListSources(ctx context.Context, cluster string) ([]unstructured.Unstructured, error) {
	if s.Config == nil || len(validation.IsDNS1123Label(cluster)) != 0 {
		return nil, errors.New("invalid source workspace")
	}
	config := rest.CopyConfig(s.Config)
	config.Host += "/clusters/" + cluster
	api, err := NewDynamic(config)
	if err != nil {
		return nil, errors.New("source API client unavailable")
	}
	var result []unstructured.Unstructured
	continuation := ""
	seen := map[string]bool{}
	for {
		page, err := api.Resource(chatResource).List(ctx, metav1.ListOptions{Limit: 200, Continue: continuation})
		if err != nil {
			return nil, err
		}
		result = append(result, page.Items...)
		if len(result) > 1000 {
			return nil, errors.New("source inventory exceeds enrollment limit")
		}
		continuation = page.GetContinue()
		if continuation == "" {
			return result, nil
		}
		if seen[continuation] {
			return nil, errors.New("source inventory pagination did not progress")
		}
		seen[continuation] = true
	}
}

type Managed struct{ API dynamic.Interface }

func (m Managed) GetManaged(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error) {
	return m.API.Resource(chatResource).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
}

func (m Managed) HasRuntime(ctx context.Context, namespace, name string) (bool, error) {
	if len(validation.IsDNS1123Label(namespace)) != 0 || len(validation.IsDNS1123Subdomain(name+"-chatui")) != 0 {
		return false, errors.New("invalid runtime lookup coordinates")
	}
	for _, resource := range []schema.GroupVersionResource{deploymentResource, ingressResource} {
		_, err := m.API.Resource(resource).Namespace(namespace).Get(ctx, name+"-chatui", metav1.GetOptions{})
		if err == nil {
			return true, nil
		}
		if !apierrors.IsNotFound(err) {
			return false, err
		}
	}
	return false, nil
}

type Store struct {
	API             dynamic.Interface
	Namespace, Name string
}

func (s Store) GetEnrollment(ctx context.Context) (*corev1.ConfigMap, error) {
	u, err := s.API.Resource(configMapResource).Namespace(s.Namespace).Get(ctx, s.Name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	var config corev1.ConfigMap
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &config); err != nil {
		return nil, errors.New("enrollment ConfigMap cannot be decoded")
	}
	return &config, nil
}

func (s Store) UpdateEnrollment(ctx context.Context, original *corev1.ConfigMap, doc accesspolicy.EnrollmentDocument) error {
	if original == nil || original.Namespace != s.Namespace || original.Name != s.Name ||
		original.UID == "" || original.ResourceVersion == "" || original.DeletionTimestamp != nil {
		return errors.New("enrollment ConfigMap update identity rejected")
	}
	data, err := json.Marshal(doc)
	if err != nil || len(data) > 1<<20 {
		return errors.New("enrollment document cannot be encoded or is too large")
	}
	patch, err := json.Marshal([]map[string]any{
		{"op": "test", "path": "/metadata/uid", "value": original.UID},
		{"op": "test", "path": "/metadata/resourceVersion", "value": original.ResourceVersion},
		{"op": "replace", "path": "/data/" + accesspolicy.EnrollmentDataKey, "value": string(data)},
	})
	if err != nil {
		return errors.New("enrollment patch cannot be encoded")
	}
	_, err = s.API.Resource(configMapResource).Namespace(s.Namespace).Patch(ctx, s.Name, types.JSONPatchType, patch,
		metav1.PatchOptions{FieldManager: "chat-ui-enrollment"})
	return err
}

type Registry struct{ API dynamic.Interface }

func (r Registry) GetClient(ctx context.Context, name string) (*unstructured.Unstructured, error) {
	return r.API.Resource(clientResource).Get(ctx, name, metav1.GetOptions{})
}

func (r Registry) UpdateRedirects(ctx context.Context, original *unstructured.Unstructured, redirects, origins []string) error {
	if original == nil || original.GetUID() == "" || original.GetResourceVersion() == "" ||
		original.GetDeletionTimestamp() != nil || original.GetNamespace() != "" ||
		original.GetAPIVersion() != "openidclient.keycloak.crossplane.io/v1alpha1" || original.GetKind() != "Client" {
		return errors.New("OIDC registration update identity rejected")
	}
	patch, err := json.Marshal([]map[string]any{
		{"op": "test", "path": "/metadata/uid", "value": original.GetUID()},
		{"op": "test", "path": "/metadata/resourceVersion", "value": original.GetResourceVersion()},
		{"op": "add", "path": "/spec/forProvider/validRedirectUris", "value": redirects},
		{"op": "add", "path": "/spec/forProvider/webOrigins", "value": origins},
	})
	if err != nil {
		return errors.New("OIDC registration patch cannot be encoded")
	}
	_, err = r.API.Resource(clientResource).Patch(ctx, original.GetName(), types.JSONPatchType, patch,
		metav1.PatchOptions{FieldManager: "chat-ui-enrollment"})
	return err
}
