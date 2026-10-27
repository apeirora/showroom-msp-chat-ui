package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	uiapi "github.com/apeirora/showroom-msp-chat-ui/api/v1alpha1"
	"github.com/apeirora/showroom-msp-chat-ui/internal/hostnamepolicy"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestHostnameBoundaryAnnotationSpoof(t *testing.T) {
	inst := &uiapi.ChatUIInstance{ObjectMeta: metav1.ObjectMeta{
		Name: "attacker", Namespace: "attacker-account", UID: "attacker-uid",
		CreationTimestamp: metav1.NewTime(time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)),
		Annotations:       map[string]string{slugAnnotationKey: "victim-public-address"},
	}}
	r := hostnameReconciler(t, inst)
	slug, err := r.ensureSlug(context.Background(), inst)
	if err != nil {
		t.Fatal(err)
	}
	if slug == "victim-public-address" {
		t.Fatal("tenant annotation selected the victim's public hostname")
	}
}

func TestHostnameBoundaryForeignIngress(t *testing.T) {
	inst := &uiapi.ChatUIInstance{ObjectMeta: metav1.ObjectMeta{
		Name: "demo", Namespace: "tenant", UID: "instance-uid",
	}}
	ing := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{
		Name: "demo-chatui", Namespace: "tenant",
		OwnerReferences: []metav1.OwnerReference{{APIVersion: uiapi.GroupVersion.String(),
			Kind: "ChatUIInstance", Name: "other", UID: "other-uid", Controller: ptrTo(true)}},
	}, Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{Host: "victim.example.test"}}}}
	r := hostnameReconciler(t, inst, ing)
	err := r.reconcileIngress(context.Background(), inst, uiLabels(inst.Name), ing.Name, "attacker.example.test", "/")
	if err == nil {
		t.Fatal("reconciliation replaced an Ingress controlled by another instance")
	}
	var after networkingv1.Ingress
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(ing), &after); err != nil {
		t.Fatal(err)
	}
	if after.Spec.Rules[0].Host != "victim.example.test" {
		t.Fatal("foreign Ingress changed")
	}
}

func hostnameReconciler(t *testing.T, objects ...client.Object) *ChatUIInstanceReconciler {
	t.Helper()
	sch := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(sch); err != nil {
		t.Fatal(err)
	}
	if err := uiapi.AddToScheme(sch); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(sch).WithStatusSubresource(&uiapi.ChatUIInstance{}).
		WithObjects(objects...).Build()
	p, err := hostnamepolicy.Parse(strings.NewReader(`{"cutover":"2026-10-26T00:00:00Z"}`))
	if err != nil {
		t.Fatal(err)
	}
	return &ChatUIInstanceReconciler{Client: c, Scheme: sch, HostnamePolicy: p, PublicHost: "example.test", PublicScheme: "https"}
}
