package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	uiapi "github.com/apeirora/showroom-msp-chat-ui/api/v1alpha1"
	"github.com/apeirora/showroom-msp-chat-ui/internal/hostnamepolicy"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func hostnameInstance(name, namespace, uid string) *uiapi.ChatUIInstance {
	return &uiapi.ChatUIInstance{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: namespace, UID: types.UID(uid), Generation: 1,
		CreationTimestamp: metav1.NewTime(time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)),
		Finalizers:        []string{"ui.privatellms.msp/chatuiinstance-finalizer"},
		Annotations:       map[string]string{slugAnnotationKey: "copied-victim-name"},
	}, Spec: uiapi.ChatUIInstanceSpec{CredentialsSecretRef: uiapi.CredentialsSecretReference{Name: "credentials"}}}
}

func hostnameCredentials(namespace string) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "credentials", Namespace: namespace},
		Data: map[string][]byte{"OPENAI_API_KEY": []byte("synthetic"), "OPENAI_API_URL": []byte("https://example.test")}}
}

func reconcileHostname(t *testing.T, r *ChatUIInstanceReconciler, inst *uiapi.ChatUIInstance) {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(inst)}); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(inst), inst); err != nil {
		t.Fatal(err)
	}
}

func getHostnameIngress(t *testing.T, r *ChatUIInstanceReconciler, inst *uiapi.ChatUIInstance) *networkingv1.Ingress {
	t.Helper()
	var ing networkingv1.Ingress
	if err := r.Get(context.Background(), client.ObjectKey{Namespace: inst.Namespace, Name: inst.Name + "-chatui"}, &ing); err != nil {
		t.Fatal(err)
	}
	return &ing
}

func TestHostnameTenantMetadataCannotChangeEitherRoute(t *testing.T) {
	a, b := hostnameInstance("ui", "account-a", "uid-a"), hostnameInstance("ui", "account-b", "uid-b")
	b.Status.Slug, b.Status.SlugSource = "copied-victim-name", hostnamepolicy.Legacy
	r := hostnameReconciler(t, a, b, hostnameCredentials(a.Namespace), hostnameCredentials(b.Namespace))
	for _, inst := range []*uiapi.ChatUIInstance{a, b} {
		reconcileHostname(t, r, inst)
		// Settle the existing managed-annotation reconciliation before the attack.
		reconcileHostname(t, r, inst)
		if inst.Annotations[slugAnnotationKey] != "copied-victim-name" || inst.Status.SlugSource != hostnamepolicy.Derived {
			t.Fatal("controller wrote source metadata or trusted spoofed status")
		}
	}
	aBefore, bBefore := getHostnameIngress(t, r, a), getHostnameIngress(t, r, b)
	if aBefore.Spec.Rules[0].Host == bBefore.Spec.Rules[0].Host {
		t.Fatal("two tenant namespaces selected the same host")
	}
	b.Annotations[slugAnnotationKey] = a.Status.Slug
	b.Labels = map[string]string{slugAnnotationKey: a.Status.Slug}
	if err := r.Update(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	reconcileHostname(t, r, b)
	if !reflect.DeepEqual(aBefore, getHostnameIngress(t, r, a)) || !reflect.DeepEqual(bBefore, getHostnameIngress(t, r, b)) {
		t.Fatal("annotation edit changed an existing route")
	}
}

func TestHostnameLegacyMappingAndUnmappedDeletion(t *testing.T) {
	for _, mapped := range []bool{false, true} {
		t.Run(fmt.Sprint(mapped), func(t *testing.T) {
			inst := hostnameInstance("legacy", "account-a", "legacy-uid")
			inst.CreationTimestamp.Time = inst.CreationTimestamp.Add(-48 * time.Hour)
			r := hostnameReconciler(t, inst, hostnameCredentials(inst.Namespace))
			ing := r.buildDesiredIngress(inst, uiLabels(inst.Name), inst.Name+"-chatui", "verified-legacy.example.test", "/")
			if err := ctrl.SetControllerReference(inst, ing, r.Scheme); err != nil {
				t.Fatal(err)
			}
			if err := r.Create(context.Background(), ing); err != nil {
				t.Fatal(err)
			}
			if mapped {
				var err error
				r.HostnamePolicy, err = hostnamepolicy.Parse(strings.NewReader(`{"cutover":"2026-10-26T00:00:00Z","entries":[
				{"uid":"legacy-uid","namespace":"account-a","name":"legacy","mode":"legacy","slug":"verified-legacy"}]}`))
				if err != nil {
					t.Fatal(err)
				}
			}
			counter := &imagePolicyClient{Client: r.Client}
			r.Client = counter
			reconcileHostname(t, r, inst)
			if mapped && !reflect.DeepEqual(ing.Spec, getHostnameIngress(t, r, inst).Spec) {
				t.Fatal("legacy route changed")
			}
			if mapped {
				if inst.Status.Slug != "verified-legacy" || inst.Status.SlugSource != hostnamepolicy.Legacy {
					t.Fatal("legacy ownership mapping not used")
				}
			} else {
				var withdrawn networkingv1.Ingress
				if err := r.Get(context.Background(), client.ObjectKeyFromObject(ing), &withdrawn); !apierrors.IsNotFound(err) {
					t.Fatalf("unmapped route not withdrawn: %v", err)
				}
				condition := meta.FindStatusCondition(inst.Status.Conditions, "Ready")
				if counter.secretReads != 0 || condition == nil || condition.Reason != "LegacyHostnameUnmapped" || inst.Status.URL != "" {
					t.Fatal("unmapped legacy object did not stop before credential access")
				}
				var deployments appsv1.DeploymentList
				if err := r.List(context.Background(), &deployments); err != nil || len(deployments.Items) != 0 {
					t.Fatal("unmapped instance created a workload")
				}
			}
			if err := r.Delete(context.Background(), inst); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(inst)}); err != nil {
				t.Fatal(err)
			}
			if err := r.Get(context.Background(), client.ObjectKeyFromObject(inst), inst); !apierrors.IsNotFound(err) {
				t.Fatalf("Chat UI deletion blocked on hostname mapping: %v", err)
			}
		})
	}
}

func TestHostnameConflictAcrossNamespacesAndWildcards(t *testing.T) {
	for _, wildcard := range []bool{false, true} {
		inst := hostnameInstance("new", "account-a", "new-uid")
		r := hostnameReconciler(t, inst, hostnameCredentials(inst.Namespace))
		selection, err := r.HostnamePolicy.Select(inst)
		if err != nil {
			t.Fatal(err)
		}
		host := selection.Slug + ".example.test"
		if wildcard {
			host = "*.example.test"
		}
		foreign := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "private-other-name", Namespace: "other-account"},
			Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{Host: host}}}}
		if err := r.Create(context.Background(), foreign); err != nil {
			t.Fatal(err)
		}
		counter := &imagePolicyClient{Client: r.Client}
		r.Client = counter
		reconcileHostname(t, r, inst)
		condition := meta.FindStatusCondition(inst.Status.Conditions, "Ready")
		if condition == nil || condition.Reason != "HostnameConflict" || condition.Status != metav1.ConditionFalse ||
			strings.Contains(condition.Message, foreign.Name) || counter.secretReads != 0 {
			t.Fatal("conflict was not denied before credentials with a safe status")
		}
		var ingresses networkingv1.IngressList
		if err := r.List(context.Background(), &ingresses); err != nil || len(ingresses.Items) != 1 ||
			!reflect.DeepEqual(ingresses.Items[0].Spec, foreign.Spec) {
			t.Fatal("conflict created a route or changed the foreign route")
		}
		if err := r.Delete(context.Background(), foreign); err != nil {
			t.Fatal(err)
		}
		reconcileHostname(t, r, inst)
		if getHostnameIngress(t, r, inst).Spec.Rules[0].Host != selection.Slug+".example.test" {
			t.Fatal("instance did not recover after conflict removal")
		}
	}
}

type failingRouteReader struct{ client.Reader }

func (f failingRouteReader) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return errors.New("synthetic API outage with internal details")
}

func TestHostnameUncachedReadFailureDoesNotPublishOrLeak(t *testing.T) {
	inst := hostnameInstance("new", "account-a", "new-uid")
	r := hostnameReconciler(t, inst)
	counter := &imagePolicyClient{Client: r.Client}
	r.RouteReader, r.Client = failingRouteReader{Reader: r.Client}, counter
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(inst)}); err == nil {
		t.Fatal("ignored authoritative route read failure")
	}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(inst), inst); err != nil {
		t.Fatal(err)
	}
	if counter.secretReads != 0 || strings.Contains(fmt.Sprint(inst.Status), "internal details") {
		t.Fatal("API failure continued to credentials or leaked into status")
	}
}

type lateHostnameReader struct {
	client.Client
	reads    int
	conflict *networkingv1.Ingress
}

func (r *lateHostnameReader) List(ctx context.Context, objects client.ObjectList, options ...client.ListOption) error {
	if _, ok := objects.(*networkingv1.IngressList); ok {
		r.reads++
		if r.reads == 2 {
			if r.conflict == nil {
				return errors.New("synthetic late hostname read failure")
			}
			if err := r.Create(ctx, r.conflict); err != nil {
				return err
			}
		}
	}
	return r.Client.List(ctx, objects, options...)
}

func TestHostnameLateFailureWithdrawsExistingAccess(t *testing.T) {
	const ready = "Ready"
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprint(conflict), func(t *testing.T) {
			ctx := context.Background()
			inst := hostnameInstance("ui", "tenant", "uid")
			r := hostnameReconciler(t, inst, hostnameCredentials(inst.Namespace))
			reconcileHostname(t, r, inst)
			reconcileHostname(t, r, inst)
			ingress := getHostnameIngress(t, r, inst)
			inst.Status.URL, inst.Status.Phase = "https://"+ingress.Spec.Rules[0].Host, ready
			meta.SetStatusCondition(&inst.Status.Conditions, metav1.Condition{
				Type: ready, Status: metav1.ConditionTrue, Reason: ready, Message: ready,
			})
			if err := r.Status().Update(ctx, inst); err != nil {
				t.Fatal(err)
			}
			reader := &lateHostnameReader{Client: r.Client}
			if conflict {
				reader.conflict = &networkingv1.Ingress{
					ObjectMeta: metav1.ObjectMeta{Name: "foreign", Namespace: "other"},
					Spec:       networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{Host: ingress.Spec.Rules[0].Host}}},
				}
			}
			r.RouteReader = reader
			_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(inst)})
			if reader.reads != 2 || (err == nil) != conflict {
				t.Fatalf("did not exercise late hostname failure: reads=%d err=%v", reader.reads, err)
			}
			key := client.ObjectKeyFromObject(ingress)
			if err := r.Get(ctx, key, &networkingv1.Ingress{}); !apierrors.IsNotFound(err) {
				t.Fatalf("late hostname failure retained existing route: %v", err)
			}
			var deployment appsv1.Deployment
			if err := r.Get(ctx, key, &deployment); err != nil {
				t.Fatal(err)
			}
			if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 0 {
				t.Fatal("late hostname failure retained running workload")
			}
			if err := r.Get(ctx, client.ObjectKeyFromObject(inst), inst); err != nil {
				t.Fatal(err)
			}
			if inst.Status.URL != "" || !meta.IsStatusConditionFalse(inst.Status.Conditions, ready) {
				t.Fatal("late hostname failure retained ready status")
			}
			if conflict {
				var foreign networkingv1.Ingress
				if err := r.Get(ctx, client.ObjectKeyFromObject(reader.conflict), &foreign); err != nil ||
					!reflect.DeepEqual(foreign.Spec, reader.conflict.Spec) {
					t.Fatal("late hostname failure changed foreign route")
				}
			}
		})
	}
}

func TestHostnameOwnedRouteConvergesToOneBackend(t *testing.T) {
	inst := hostnameInstance("demo", "account-a", "demo-uid")
	r := hostnameReconciler(t, inst)
	host := "approved.example.test"
	ing := r.buildDesiredIngress(inst, uiLabels(inst.Name), inst.Name+"-chatui", host, "/")
	ing.Spec.Rules = append(ing.Spec.Rules, networkingv1.IngressRule{Host: "stale-unapproved.example.test"})
	ing.Spec.Rules[0].HTTP.Paths = append(ing.Spec.Rules[0].HTTP.Paths, networkingv1.HTTPIngressPath{Path: "/stale"})
	ing.Spec.DefaultBackend = &networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "stale"}}
	if err := ctrl.SetControllerReference(inst, ing, r.Scheme); err != nil {
		t.Fatal(err)
	}
	if err := r.Create(context.Background(), ing); err != nil {
		t.Fatal(err)
	}
	if err := r.reconcileIngress(context.Background(), inst, uiLabels(inst.Name), ing.Name, host, "/"); err != nil {
		t.Fatal(err)
	}
	actual := getHostnameIngress(t, r, inst)
	if len(actual.Spec.Rules) != 1 || len(actual.Spec.Rules[0].HTTP.Paths) != 1 || actual.Spec.DefaultBackend != nil {
		t.Fatal("owned Ingress retained additional hosts or backends")
	}
}
