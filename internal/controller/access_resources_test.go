package controller

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	uiapi "github.com/apeirora/showroom-msp-chat-ui/api/v1alpha1"
	"github.com/apeirora/showroom-msp-chat-ui/internal/accesspolicy"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func configureTestAccess(t *testing.T, r *ChatUIInstanceReconciler, instances ...*uiapi.ChatUIInstance) {
	t.Helper()
	if r.AccessPolicy == nil {
		r.AccessPolicy = &accesspolicy.Policy{KCPServer: "https://kcp.example.test", CredentialsNamespace: "operator", ProxyImage: "example.test/controller@sha256:" + strings.Repeat("a", 64)}
	}
	for _, inst := range instances {
		r.AccessPolicy.Entries = append(r.AccessPolicy.Entries, accesspolicy.Entry{UID: string(inst.UID), Namespace: inst.Namespace, Name: inst.Name,
			Source: accesspolicy.Source{Cluster: "account", Namespace: "default", Name: inst.Name, UID: "source-" + string(inst.UID)},
			Issuer: "https://issuer.example.test/realms/test", ClientID: "chat-ui", ClientSecretName: "oidc-client"})
	}
	for _, obj := range []client.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "operator"}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "oidc-client", Namespace: "operator"}, Data: map[string][]byte{"client_secret": []byte("synthetic-client-secret-value")}},
	} {
		if err := r.Create(context.Background(), obj); err != nil && !apierrors.IsAlreadyExists(err) {
			t.Fatal(err)
		}
	}
}

func TestAccessSecretsRandomOwnedImmutableAndStable(t *testing.T) {
	inst := hostnameInstance("ui", "tenant", "uid")
	r := hostnameReconciler(t, inst)
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(inst), inst); err != nil {
		t.Fatal(err)
	}
	b, err := r.prepareAccess(context.Background(), inst, "ui.example.test")
	if err != nil {
		t.Fatal(err)
	}
	var before corev1.Secret
	if err := r.Get(context.Background(), client.ObjectKey{Namespace: inst.Namespace, Name: b.sessionName}, &before); err != nil {
		t.Fatal(err)
	}
	if !ownedByInstance(&before, inst) || before.Immutable == nil || !*before.Immutable || reflect.DeepEqual(before.Data["cookie-secret"], before.Data["webui-secret"]) {
		t.Fatal("unsafe key material")
	}
	if _, err := r.prepareAccess(context.Background(), inst, "ui.example.test"); err != nil {
		t.Fatal(err)
	}
	var after corev1.Secret
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(&before), &after); err != nil || !reflect.DeepEqual(before.Data, after.Data) {
		t.Fatal("session material changed on reconcile")
	}
	before.OwnerReferences = nil
	if err := r.Update(context.Background(), &before); err != nil {
		t.Fatal(err)
	}
	if _, err := r.prepareAccess(context.Background(), inst, "ui.example.test"); err == nil {
		t.Fatal("adopted unowned session Secret")
	}
}

func TestMissingAccessWithdrawsOwnedRouteAndStopsWorkload(t *testing.T) {
	inst := hostnameInstance("ui", "tenant", "uid")
	r := hostnameReconciler(t, inst, hostnameCredentials(inst.Namespace))
	reconcileHostname(t, r, inst)
	_ = getHostnameIngress(t, r, inst)
	r.AccessPolicy.Entries = nil
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(inst)}); !errors.Is(err, accesspolicy.ErrUnmapped) {
		t.Fatalf("missing map did not deny: %v", err)
	}
	var ing networkingv1.Ingress
	if err := r.Get(context.Background(), client.ObjectKey{Namespace: inst.Namespace, Name: inst.Name + "-chatui"}, &ing); !apierrors.IsNotFound(err) {
		t.Fatalf("old route remained: %v", err)
	}
	var deployment appsv1.Deployment
	if err := r.Get(context.Background(), client.ObjectKey{Namespace: inst.Namespace, Name: inst.Name + "-chatui"}, &deployment); err != nil || *deployment.Spec.Replicas != 0 {
		t.Fatalf("old workload not stopped: %v", err)
	}
}

func TestMigrationFailureWithdrawsPreviousRoute(t *testing.T) {
	const foreignDeployment = "foreign-deployment"
	for _, failure := range []string{"missing-reference", "missing-secret", foreignDeployment, "route-read-error"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			inst := hostnameInstance("ui", "tenant", "uid")
			credentials := hostnameCredentials(inst.Namespace)
			r := hostnameReconciler(t, inst, credentials)
			reconcileHostname(t, r, inst)
			inst.Status.URL = "https://previous.example.test"
			meta.SetStatusCondition(&inst.Status.Conditions, metav1.Condition{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Ready", Message: "Previous controller"})
			if err := r.Status().Update(ctx, inst); err != nil {
				t.Fatal(err)
			}
			key := client.ObjectKey{Namespace: inst.Namespace, Name: inst.Name + "-chatui"}
			var deployment appsv1.Deployment
			if err := r.Get(ctx, key, &deployment); err != nil {
				t.Fatal(err)
			}
			// Same approved image can still be the prior unauthenticated template.
			deployment.Spec.Template.Spec.Containers = deployment.Spec.Template.Spec.Containers[:1]
			deployment.Spec.Template.Spec.Containers[0].Env = nil
			if failure == foreignDeployment {
				deployment.OwnerReferences = nil
			}
			if err := r.Update(ctx, &deployment); err != nil {
				t.Fatal(err)
			}
			switch failure {
			case "missing-reference":
				inst.Spec.CredentialsSecretRef.Name = ""
				if err := r.Update(ctx, inst); err != nil {
					t.Fatal(err)
				}
			case "missing-secret":
				if err := r.Delete(ctx, credentials); err != nil {
					t.Fatal(err)
				}
			case "route-read-error":
				r.RouteReader = failingRouteReader{Reader: r.Client}
			}
			_, _ = r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(inst)})
			var ing networkingv1.Ingress
			if err := r.Get(ctx, key, &ing); !apierrors.IsNotFound(err) {
				t.Fatalf("old route retained: %v", err)
			}
			if err := r.Get(ctx, key, &deployment); err != nil {
				t.Fatal(err)
			}
			if failure != foreignDeployment && *deployment.Spec.Replicas != 0 {
				t.Fatal("old workload left running")
			}
			if failure == foreignDeployment && *deployment.Spec.Replicas != 1 {
				t.Fatal("foreign workload changed")
			}
			if err := r.Get(ctx, client.ObjectKeyFromObject(inst), inst); err != nil {
				t.Fatal(err)
			}
			if inst.Status.URL != "" || meta.IsStatusConditionTrue(inst.Status.Conditions, "Ready") {
				t.Fatal("stale ready URL retained")
			}
		})
	}
}

func TestAccessBundleHasNoCallerSelectedIdentity(t *testing.T) {
	inst := hostnameInstance("ui", "tenant", "uid")
	inst.Annotations["syncagent.kcp.io/remote-object-cluster"] = "victim"
	r := hostnameReconciler(t, inst)
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(inst), inst); err != nil {
		t.Fatal(err)
	}
	b, err := r.prepareAccess(context.Background(), inst, "ui.example.test")
	if err != nil {
		t.Fatal(err)
	}
	var secret corev1.Secret
	if err := r.Get(context.Background(), client.ObjectKey{Namespace: inst.Namespace, Name: b.name}, &secret); err != nil {
		t.Fatal(err)
	}
	var gate map[string]any
	if err := json.Unmarshal(secret.Data["gate.json"], &gate); err != nil {
		t.Fatal(err)
	}
	if gate["source"].(map[string]any)["cluster"] != "account" {
		t.Fatal("tenant metadata selected authorization target")
	}
	if strings.Contains(string(secret.Data["gate.json"]), "synthetic-client-secret") {
		t.Fatal("client credential leaked into gate config")
	}
}

func TestAccessSecretPinsRejectRecreationWithCopiedOwnership(t *testing.T) {
	r := hostnameAPIServer(t, false)
	ctx := context.Background()
	ns := "access-pins"
	if err := r.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
		t.Fatal(err)
	}
	inst := hostnameInstance("ui", ns, "")
	inst.CreationTimestamp = metav1.Time{}
	if err := r.Create(ctx, inst); err != nil {
		t.Fatal(err)
	}
	configureTestAccess(t, r, inst)
	b, err := r.prepareAccess(ctx, inst, "ui.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if len(inst.Status.AccessSecretUIDs) != 2 {
		t.Fatal("API pruned secret pins")
	}
	var original corev1.Secret
	if err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: b.sessionName}, &original); err != nil {
		t.Fatal(err)
	}
	if err := r.Delete(ctx, &original); err != nil {
		t.Fatal(err)
	}
	original.UID = ""
	original.ResourceVersion = ""
	if err := r.Create(ctx, &original); err != nil {
		t.Fatal(err)
	}
	if _, err := r.prepareAccess(ctx, inst, "ui.example.test"); err == nil {
		t.Fatal("recreated signing Secret accepted with copied ownership and data")
	}
}

func TestAccessCutoverWaitsForActualPodsAndWithdrawsRoute(t *testing.T) {
	ctx := context.Background()
	inst := hostnameInstance("ui", "tenant", "uid")
	r := hostnameReconciler(t, inst, hostnameCredentials(inst.Namespace))
	reconcileHostname(t, r, inst)
	var deployment appsv1.Deployment
	key := client.ObjectKey{Namespace: inst.Namespace, Name: inst.Name + "-chatui"}
	if err := r.Get(ctx, key, &deployment); err != nil {
		t.Fatal(err)
	}
	deployment.Spec.Replicas = ptrTo(int32(0))
	deployment.Status.Replicas = 0
	deployment.Status.ObservedGeneration = deployment.Generation
	if err := r.Update(ctx, &deployment); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "old-pod", Namespace: inst.Namespace, Labels: deployment.Spec.Selector.MatchLabels}}
	if err := r.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	waiting, err := r.waitForAccessCutover(ctx, inst, &deployment)
	if err != nil || !waiting {
		t.Fatalf("accepted zero-replica status while prior pod exists: %v", err)
	}
	var ing networkingv1.Ingress
	if err := r.Get(ctx, key, &ing); !apierrors.IsNotFound(err) {
		t.Fatal("route remained during cutover")
	}
	if err := r.Delete(ctx, pod); err != nil {
		t.Fatal(err)
	}
	waiting, err = r.waitForAccessCutover(ctx, inst, &deployment)
	if err != nil || waiting {
		t.Fatalf("cutover blocked after pods stopped: %v", err)
	}
}
