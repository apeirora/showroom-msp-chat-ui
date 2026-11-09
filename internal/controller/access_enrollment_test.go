package controller

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/apeirora/showroom-msp-chat-ui/internal/accesspolicy"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestTrustedEnrollmentStartsNewInstanceAndRevocationStopsIt(t *testing.T) {
	ctx := context.Background()
	namespace := accesspolicy.RuntimeNamespace("account", "default")
	inst := hostnameInstance("chat", namespace, "managed-uid")
	r := hostnameReconciler(t, inst, hostnameCredentials(namespace))
	entry := r.AccessPolicy.Entries[0]
	r.AccessPolicy.Entries = nil
	r.AccessPolicy.Enrollment = &accesspolicy.Enrollment{ConfigMapName: "trusted-enrollment", Workspaces: []accesspolicy.Workspace{{
		Cluster: entry.Source.Cluster, Issuer: entry.Issuer, ClientID: entry.ClientID, ClientSecretName: entry.ClientSecretName,
	}}}
	doc := accesspolicy.EnrollmentDocument{Version: 1, Entries: []accesspolicy.Entry{entry}, Pins: []accesspolicy.Entry{entry}}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	config := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "trusted-enrollment", Namespace: "operator", UID: "config-uid",
		Labels: map[string]string{"app.kubernetes.io/managed-by": "chat-ui-enrollment"}},
		Data: map[string]string{accesspolicy.EnrollmentDataKey: string(data)}}
	if err := r.Create(ctx, config); err != nil {
		t.Fatal(err)
	}
	reconcileHostname(t, r, inst)
	_ = getHostnameIngress(t, r, inst)
	if err := r.Get(ctx, client.ObjectKeyFromObject(config), config); err != nil {
		t.Fatal(err)
	}
	doc.Entries = nil // Keep the source pin even while the grant is withdrawn.
	data, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	config.Data[accesspolicy.EnrollmentDataKey] = string(data)
	if err := r.Update(ctx, config); err != nil {
		t.Fatal(err)
	}
	if result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(inst)}); err != nil || result.RequeueAfter != hostnameRecheckAfter {
		t.Fatalf("revocation did not retry after withdrawing access: %v", err)
	}
	key := client.ObjectKey{Namespace: namespace, Name: inst.Name + "-chatui"}
	if err := r.Get(ctx, key, &networkingv1.Ingress{}); !apierrors.IsNotFound(err) {
		t.Fatalf("revoked route remained: %v", err)
	}
	var deployment appsv1.Deployment
	if err := r.Get(ctx, key, &deployment); err != nil || deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 0 {
		t.Fatalf("revoked workload remained active: %v", err)
	}
}

func TestStaticAccessDoesNotDependOnEnrollmentAvailability(t *testing.T) {
	inst := hostnameInstance("chat", "tenant", "managed-uid")
	r := hostnameReconciler(t, inst)
	r.AccessPolicy.Enrollment = &accesspolicy.Enrollment{ConfigMapName: "missing-config"}
	entry, err := r.selectAccessEntry(context.Background(), inst)
	if err != nil || entry != r.AccessPolicy.Entries[0] {
		t.Fatalf("existing reviewed mapping was disrupted: %v", err)
	}
	r.AccessPolicy.Entries = nil
	if _, err := r.selectAccessEntry(context.Background(), inst); err == nil {
		t.Fatal("missing enrollment configuration allowed a new instance")
	}
}

// Only the ConfigMap read fails; successful route and Deployment API operations
// let us verify the precise outage response instead of conflating API failures.
type enrollmentReadFailure struct{ client.Reader }

func (r enrollmentReadFailure) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*corev1.ConfigMap); ok {
		return apierrors.NewServiceUnavailable("synthetic unavailable")
	}
	return r.Reader.Get(ctx, key, obj, opts...)
}

func TestEnrollmentReadOutageWithdrawsRouteWithoutRestartAndRecovers(t *testing.T) {
	ctx := context.Background()
	namespace := accesspolicy.RuntimeNamespace("account", "default")
	inst := hostnameInstance("chat", namespace, "managed-uid")
	r := hostnameReconciler(t, inst, hostnameCredentials(namespace))
	entry := r.AccessPolicy.Entries[0]
	r.AccessPolicy.Entries = nil
	r.AccessPolicy.Enrollment = &accesspolicy.Enrollment{ConfigMapName: "trusted-enrollment", Workspaces: []accesspolicy.Workspace{{
		Cluster: entry.Source.Cluster, Issuer: entry.Issuer, ClientID: entry.ClientID, ClientSecretName: entry.ClientSecretName,
	}}}
	doc := accesspolicy.EnrollmentDocument{Version: 1, Entries: []accesspolicy.Entry{entry}, Pins: []accesspolicy.Entry{entry}}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	config := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "trusted-enrollment", Namespace: "operator", UID: "config-uid",
		Labels: map[string]string{"app.kubernetes.io/managed-by": "chat-ui-enrollment"}},
		Data: map[string]string{accesspolicy.EnrollmentDataKey: string(data)}}
	if err := r.Create(ctx, config); err != nil {
		t.Fatal(err)
	}
	reconcileHostname(t, r, inst)
	_ = getHostnameIngress(t, r, inst)
	key := client.ObjectKey{Namespace: namespace, Name: inst.Name + "-chatui"}
	var before, after appsv1.Deployment
	if err := r.Get(ctx, key, &before); err != nil {
		t.Fatal(err)
	}
	r.RouteReader = enrollmentReadFailure{Reader: r.Client}
	result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(inst)})
	if err != nil || result.RequeueAfter != hostnameRecheckAfter {
		t.Fatalf("outage did not retry promptly: %v", err)
	}
	if err := r.Get(ctx, key, &networkingv1.Ingress{}); !apierrors.IsNotFound(err) {
		t.Fatalf("outage kept a public route: %v", err)
	}
	if err := r.Get(ctx, key, &after); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Spec, after.Spec) {
		t.Fatal("transient map read failure changed the workload")
	}
	r.RouteReader = r.Client
	reconcileHostname(t, r, inst)
	_ = getHostnameIngress(t, r, inst)
	if err := r.Get(ctx, key, &after); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Spec, after.Spec) {
		t.Fatal("recovery unnecessarily restarted the workload")
	}
	if err := r.Delete(ctx, config); err != nil {
		t.Fatal(err)
	}
	_, _ = r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(inst)})
	if err := r.Get(ctx, key, &after); err != nil || after.Spec.Replicas == nil || *after.Spec.Replicas != 0 {
		t.Fatalf("confirmed missing map did not stop the workload: %v", err)
	}
}
