package controller

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	uiapi "github.com/apeirora/showroom-msp-chat-ui/api/v1alpha1"
	"github.com/apeirora/showroom-msp-chat-ui/internal/hostnamepolicy"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"
)

func hostnameAPIServer(t *testing.T, oldSchema bool) *ChatUIInstanceReconciler {
	t.Helper()
	environment := &envtest.Environment{CRDDirectoryPaths: []string{"../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
	if oldSchema {
		data, err := os.ReadFile("../../config/crd/bases/ui.privatellms.msp_chatuiinstances.yaml")
		if err != nil {
			t.Fatal(err)
		}
		var crd apiextensionsv1.CustomResourceDefinition
		if err := yaml.Unmarshal(data, &crd); err != nil {
			t.Fatal(err)
		}
		for i := range crd.Spec.Versions {
			properties := crd.Spec.Versions[i].Schema.OpenAPIV3Schema.Properties["status"].Properties
			delete(properties, "slug")
			delete(properties, "slugSource")
		}
		environment.CRDDirectoryPaths = nil
		environment.CRDs = []*apiextensionsv1.CustomResourceDefinition{&crd}
	}
	cfg, err := environment.Start()
	if err != nil {
		t.Fatalf("start required envtest: %v", err)
	}
	t.Cleanup(func() {
		if err := environment.Stop(); err != nil {
			t.Errorf("stop envtest: %v", err)
		}
	})
	sch := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(sch); err != nil {
		t.Fatal(err)
	}
	if err := uiapi.AddToScheme(sch); err != nil {
		t.Fatal(err)
	}
	c, err := client.New(cfg, client.Options{Scheme: sch})
	if err != nil {
		t.Fatal(err)
	}
	p, err := hostnamepolicy.Parse(strings.NewReader(`{"cutover":"2000-01-01T00:00:00Z"}`))
	if err != nil {
		t.Fatal(err)
	}
	return &ChatUIInstanceReconciler{Client: c, RouteReader: c, Scheme: sch,
		HostnamePolicy: p, PublicHost: "example.test", PublicScheme: "https"}
}

// Real API behavior: server-assigned identities, status persistence and tenant
// metadata edits in separate namespaces. This does not emulate KCP or Traefik.
func TestHostnamePolicyWithAPIServer(t *testing.T) {
	r := hostnameAPIServer(t, false)
	c := r.Client
	ctx := context.Background()
	instances := make([]*uiapi.ChatUIInstance, 0, 2)
	for _, namespace := range []string{"hostname-a", "hostname-b"} {
		if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}); err != nil {
			t.Fatal(err)
		}
		inst := hostnameInstance("ui", namespace, "")
		inst.CreationTimestamp = metav1.Time{}
		if err := c.Create(ctx, inst); err != nil {
			t.Fatal(err)
		}
		if inst.UID == "" || inst.CreationTimestamp.IsZero() {
			t.Fatal("API server did not assign identity")
		}
		configureTestAccess(t, r, inst)
		if err := c.Create(ctx, hostnameCredentials(namespace)); err != nil {
			t.Fatal(err)
		}
		reconcileHostname(t, r, inst)
		reconcileHostname(t, r, inst)
		if inst.Status.Slug == "" || inst.Status.SlugSource != hostnamepolicy.Derived {
			t.Fatal("served schema pruned selected hostname status")
		}
		instances = append(instances, inst)
	}
	a, b := instances[0], instances[1]
	aRoute, bRoute := getHostnameIngress(t, r, a), getHostnameIngress(t, r, b)
	if aRoute.Spec.Rules[0].Host == bRoute.Spec.Rules[0].Host {
		t.Fatal("two namespaces acquired the same hostname")
	}
	b.Annotations[slugAnnotationKey] = a.Status.Slug
	b.Labels = map[string]string{slugAnnotationKey: a.Status.Slug}
	b.Status.Slug = a.Status.Slug // Normal resource Update must not change status.
	if err := c.Update(ctx, b); err != nil {
		t.Fatal(err)
	}
	reconcileHostname(t, r, b)
	if !reflect.DeepEqual(aRoute.Spec, getHostnameIngress(t, r, a).Spec) ||
		!reflect.DeepEqual(bRoute.Spec, getHostnameIngress(t, r, b).Spec) || b.Status.Slug == a.Status.Slug {
		t.Fatal("tenant metadata/status spoof changed routing")
	}
	b.Status.SlugSource = "Tenant"
	if err := c.Status().Update(ctx, b); !apierrors.IsInvalid(err) {
		t.Fatalf("schema accepted unsupported slug source: %v", err)
	}

	// The exact Ingress updated by reconciliation must retain the expected owner.
	aRoute.OwnerReferences[0].UID = "foreign-controller-uid"
	if err := c.Update(ctx, aRoute); err != nil {
		t.Fatal(err)
	}
	reconcileHostname(t, r, a)
	var after networkingv1.Ingress
	if err := c.Get(ctx, client.ObjectKeyFromObject(aRoute), &after); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(aRoute.Spec, after.Spec) || a.Status.URL != "" || a.Status.Phase != phaseError {
		t.Fatal("foreign-owned route was adopted or advertised")
	}
}

func TestHostnameOldSchemaStopsWithoutStatusLoop(t *testing.T) {
	r := hostnameAPIServer(t, true)
	ctx := context.Background()
	ns := "old-schema"
	if err := r.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
		t.Fatal(err)
	}
	inst := hostnameInstance("ui", ns, "")
	inst.CreationTimestamp = metav1.Time{}
	if err := r.Create(ctx, inst); err != nil {
		t.Fatal(err)
	}
	inst.Status.URL, inst.Status.Phase = "https://old.example.test", "Ready"
	if err := r.Status().Update(ctx, inst); err != nil {
		t.Fatal(err)
	}
	counter := &imagePolicyClient{Client: r.Client}
	r.Client = counter
	var resourceVersion string
	for i := 0; i < 5; i++ {
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(inst)})
		if err == nil || !strings.Contains(err.Error(), "schema must persist") {
			t.Fatalf("old schema did not stop reconciliation: %v", err)
		}
		var actual uiapi.ChatUIInstance
		if err := r.Get(ctx, client.ObjectKeyFromObject(inst), &actual); err != nil {
			t.Fatal(err)
		}
		if actual.Status.Slug != "" || actual.Status.SlugSource != "" ||
			actual.Status.Phase != phaseProvisioning || actual.Status.URL != "" {
			t.Fatalf("unexpected status under old schema: %+v", actual.Status)
		}
		if i > 0 && actual.ResourceVersion != resourceVersion {
			t.Fatal("old schema produced repeated writes/watch events")
		}
		resourceVersion = actual.ResourceVersion
	}
	var routes networkingv1.IngressList
	if err := r.List(ctx, &routes); err != nil {
		t.Fatal(err)
	}
	if len(routes.Items) != 0 || counter.secretReads != 0 {
		t.Fatal("old schema proceeded to credentials or routing")
	}
}
