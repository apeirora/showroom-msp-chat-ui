package controller

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"testing"
	"time"

	uiapi "github.com/apeirora/showroom-msp-chat-ui/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

const (
	foreignOwnerDamage = "foreign owner"
	mutableDamage      = "mutable"
	recreatedDamage    = "recreated"
	rotatedHistoryCA   = "ca-3"
)

func historyInstance(t *testing.T) (*ChatUIInstanceReconciler, *uiapi.ChatUIInstance) {
	t.Helper()
	inst := hostnameInstance("history", "tenant", "history-uid")
	r := hostnameReconciler(t, inst)
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(inst), inst); err != nil {
		t.Fatal(err)
	}
	return r, inst
}

func prepareHistory(t *testing.T, r *ChatUIInstanceReconciler, inst *uiapi.ChatUIInstance, ca string) corev1.Secret {
	t.Helper()
	r.AccessPolicy.KCPCA = ca
	bundle, err := r.prepareAccess(context.Background(), inst, "history.example.test")
	if err != nil {
		t.Fatal(err)
	}
	var secret corev1.Secret
	if err := r.Get(context.Background(), client.ObjectKey{Namespace: inst.Namespace, Name: bundle.name}, &secret); err != nil {
		t.Fatal(err)
	}
	return secret
}

func fillHistory(t *testing.T, r *ChatUIInstanceReconciler, inst *uiapi.ChatUIInstance) []corev1.Secret {
	t.Helper()
	secrets := make([]corev1.Secret, 3)
	for i := range secrets {
		secrets[i] = prepareHistory(t, r, inst, fmt.Sprintf("ca-%d", i))
		// The fake server does not assign creation timestamps. Give the fixture
		// a deterministic age order without changing the immutable data or UID.
		secrets[i].CreationTimestamp = metav1.NewTime(time.Unix(int64(i+1), 0))
		if err := r.Update(context.Background(), &secrets[i]); err != nil {
			t.Fatal(err)
		}
	}
	return secrets
}

func secretMounts(names ...string) corev1.PodSpec {
	spec := corev1.PodSpec{}
	for i, name := range names {
		spec.Volumes = append(spec.Volumes, corev1.Volume{Name: fmt.Sprintf("bundle-%d", i),
			VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: name}}})
	}
	return spec
}

func TestAccessSecretHistoryBoundedAndRetiredBundleRecreated(t *testing.T) {
	r, inst := historyInstance(t)
	ctx := context.Background()
	initial := fillHistory(t, r, inst)
	for i := 3; i < 20; i++ {
		secret := prepareHistory(t, r, inst, fmt.Sprintf("ca-%d", i))
		secret.CreationTimestamp = metav1.NewTime(time.Unix(int64(i+1), 0))
		if err := r.Update(ctx, &secret); err != nil {
			t.Fatal(err)
		}
		var retained corev1.SecretList
		if err := r.List(ctx, &retained, client.InNamespace(inst.Namespace)); err != nil {
			t.Fatal(err)
		}
		if len(retained.Items) != 4 || len(inst.Status.AccessSecretUIDs) != 4 {
			t.Fatalf("unbounded history: %d Secrets, %d pins", len(retained.Items), len(inst.Status.AccessSecretUIDs))
		}
		for _, kept := range retained.Items {
			if inst.Status.AccessSecretUIDs[kept.Name] != string(kept.UID) {
				t.Fatal("left an unpinned bundle behind")
			}
		}
	}
	returned := prepareHistory(t, r, inst, "ca-0")
	if returned.Name != initial[0].Name || returned.UID == initial[0].UID || !reflect.DeepEqual(returned.Data, initial[0].Data) {
		t.Fatal("retired bundle was not freshly created and pinned on return")
	}
}

func TestAccessSecretHistoryProtectsDeploymentAndTerminatingPod(t *testing.T) {
	r, inst := historyInstance(t)
	ctx := context.Background()
	secrets := fillHistory(t, r, inst)
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "holder", Namespace: inst.Namespace},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: secretMounts(secrets[0].Name)}}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "old-pod", Namespace: inst.Namespace, Finalizers: []string{"test.example/hold"}},
		Spec: secretMounts(secrets[1].Name)}
	for _, obj := range []client.Object{deployment, pod} {
		if err := r.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Delete(ctx, pod); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(pod), pod); err != nil || pod.DeletionTimestamp == nil {
		t.Fatalf("fixture Pod is not terminating: %v", err)
	}
	newest := prepareHistory(t, r, inst, rotatedHistoryCA)
	for _, protected := range secrets[:2] {
		var after corev1.Secret
		if err := r.Get(ctx, client.ObjectKeyFromObject(&protected), &after); err != nil || after.UID != protected.UID || !reflect.DeepEqual(after.Data, protected.Data) {
			t.Fatalf("deleted or changed a referenced bundle: %v", err)
		}
	}
	var retired corev1.Secret
	if err := r.Get(ctx, client.ObjectKeyFromObject(&secrets[2]), &retired); !apierrors.IsNotFound(err) {
		t.Fatalf("did not retire the unused bundle: %v", err)
	}
	pod.Spec = secretMounts(secrets[1].Name, newest.Name)
	if err := r.Update(ctx, pod); err != nil {
		t.Fatal(err)
	}
	pins := maps.Clone(inst.Status.AccessSecretUIDs)
	r.AccessPolicy.KCPCA = "ca-4"
	if _, err := r.prepareAccess(ctx, inst, "history.example.test"); err == nil {
		t.Fatal("evicted a referenced bundle when history was full")
	}
	if !reflect.DeepEqual(pins, inst.Status.AccessSecretUIDs) {
		t.Fatal("failed retirement changed trust pins")
	}
}

func TestAccessSecretHistoryRejectsTampering(t *testing.T) {
	for _, damage := range []string{foreignOwnerDamage, mutableDamage, recreatedDamage, "unpinned", "changed data", "missing"} {
		t.Run(damage, func(t *testing.T) {
			r, inst := historyInstance(t)
			ctx := context.Background()
			secrets := fillHistory(t, r, inst)
			target := secrets[0].DeepCopy()
			switch damage {
			case foreignOwnerDamage:
				target.OwnerReferences[0].UID = "foreign"
			case mutableDamage:
				target.Immutable = ptrTo(false)
			case "changed data":
				target.Data["gate.json"] = []byte("modified")
			case recreatedDamage, "missing":
				if err := r.Delete(ctx, target); err != nil {
					t.Fatal(err)
				}
				if damage == recreatedDamage {
					target.UID, target.ResourceVersion = "", ""
					if err := r.Create(ctx, target); err != nil {
						t.Fatal(err)
					}
				}
			case "unpinned":
				delete(inst.Status.AccessSecretUIDs, target.Name)
				if err := r.Status().Update(ctx, inst); err != nil {
					t.Fatal(err)
				}
			}
			if damage == foreignOwnerDamage || damage == mutableDamage || damage == "changed data" {
				if err := r.Update(ctx, target); err != nil {
					t.Fatal(err)
				}
			}
			r.AccessPolicy.KCPCA = "ca-0"
			pins := maps.Clone(inst.Status.AccessSecretUIDs)
			if _, err := r.prepareAccess(ctx, inst, "history.example.test"); err == nil {
				t.Fatal("accepted damaged historical bundle on rollback")
			}
			if !reflect.DeepEqual(pins, inst.Status.AccessSecretUIDs) {
				t.Fatal("repaired a missing or invalid trust pin by adoption")
			}
			if damage == foreignOwnerDamage || damage == mutableDamage || damage == recreatedDamage {
				r.AccessPolicy.KCPCA = "ca-new"
				if _, err := r.prepareAccess(ctx, inst, "history.example.test"); err == nil {
					t.Fatal("retired a bundle with a forged identity or ownership")
				}
				var untouched corev1.Secret
				if err := r.Get(ctx, client.ObjectKeyFromObject(target), &untouched); err != nil || untouched.UID != target.UID {
					t.Fatalf("deleted a foreign or modified Secret: %v", err)
				}
			}
		})
	}
}

func TestAccessSecretHistoryStatusConflicts(t *testing.T) {
	for _, conflictAt := range []int{1, 2} {
		t.Run(fmt.Sprintf("patch-%d", conflictAt), func(t *testing.T) {
			r, inst := historyInstance(t)
			ctx := context.Background()
			secrets := fillHistory(t, r, inst)
			base := r.Client.(client.WithWatch)
			patches := 0
			r.Client = interceptor.NewClient(base, interceptor.Funcs{SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
				patches++
				if patches == conflictAt {
					return apierrors.NewConflict(uiapi.GroupVersion.WithResource("chatuiinstances").GroupResource(), inst.Name, errors.New("concurrent status update"))
				}
				return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
			}})
			r.AccessPolicy.KCPCA = rotatedHistoryCA
			if _, err := r.prepareAccess(ctx, inst, "history.example.test"); !apierrors.IsConflict(err) {
				t.Fatalf("did not surface status conflict: %v", err)
			}
			r.Client = base
			if err := r.Get(ctx, client.ObjectKeyFromObject(inst), inst); err != nil {
				t.Fatal(err)
			}
			var removed corev1.Secret
			if err := r.Get(ctx, client.ObjectKeyFromObject(&secrets[0]), &removed); !apierrors.IsNotFound(err) {
				t.Fatalf("fixture did not reach the retirement boundary: %v", err)
			}
			_, err := r.prepareAccess(ctx, inst, "history.example.test")
			if conflictAt == 1 && err != nil {
				t.Fatalf("could not finish interrupted retirement: %v", err)
			}
			if conflictAt == 2 && err == nil {
				t.Fatal("adopted an orphan created before failed pin persistence")
			}
		})
	}
}

func TestAccessSecretHistoryWaitsForDeletion(t *testing.T) {
	r, inst := historyInstance(t)
	ctx := context.Background()
	secrets := fillHistory(t, r, inst)
	oldest := secrets[0].DeepCopy()
	oldest.Finalizers = []string{"test.example/hold"}
	if err := r.Update(ctx, oldest); err != nil {
		t.Fatal(err)
	}
	pins := maps.Clone(inst.Status.AccessSecretUIDs)
	r.AccessPolicy.KCPCA = rotatedHistoryCA
	if _, err := r.prepareAccess(ctx, inst, "history.example.test"); err == nil {
		t.Fatal("forgot a pin while Secret deletion was still pending")
	}
	if !reflect.DeepEqual(pins, inst.Status.AccessSecretUIDs) {
		t.Fatal("pending deletion changed pins")
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(oldest), oldest); err != nil || oldest.DeletionTimestamp == nil {
		t.Fatalf("retirement did not request deletion: %v", err)
	}
	r.AccessPolicy.KCPCA = "ca-0"
	if _, err := r.prepareAccess(ctx, inst, "history.example.test"); err == nil {
		t.Fatal("reused a bundle whose deletion is already pending")
	}
	oldest.Finalizers = nil
	if err := r.Update(ctx, oldest); err != nil {
		t.Fatal(err)
	}
	prepareHistory(t, r, inst, rotatedHistoryCA)
}

type historyDeleteClient struct {
	client.Client
	beforeDelete func(context.Context, client.Object, ...client.DeleteOption) error
}

func (c *historyDeleteClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	if err := c.beforeDelete(ctx, obj, opts...); err != nil {
		return err
	}
	return c.Client.Delete(ctx, obj, opts...)
}

func TestAccessSecretHistoryAPIServerDeletePreconditions(t *testing.T) {
	r := hostnameAPIServer(t, false)
	ctx := context.Background()
	ns := "history-races"
	if err := r.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
		t.Fatal(err)
	}
	base := r.Client
	for _, race := range []string{"resource-version", "uid"} {
		t.Run(race, func(t *testing.T) {
			inst := hostnameInstance(race, ns, "")
			inst.CreationTimestamp = metav1.Time{}
			if err := base.Create(ctx, inst); err != nil {
				t.Fatal(err)
			}
			configureTestAccess(t, r, inst)
			for i := 0; i < 3; i++ {
				prepareHistory(t, r, inst, fmt.Sprintf("ca-%d", i))
			}
			pins := maps.Clone(inst.Status.AccessSecretUIDs)
			var replacement corev1.Secret
			r.Client = &historyDeleteClient{Client: base, beforeDelete: func(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
				options := (&client.DeleteOptions{}).ApplyOptions(opts)
				if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != obj.GetUID() ||
					options.Preconditions.ResourceVersion == nil || *options.Preconditions.ResourceVersion != obj.GetResourceVersion() {
					t.Fatal("retirement did not pin both UID and resource version")
				}
				if err := base.Get(ctx, client.ObjectKeyFromObject(obj), &replacement); err != nil {
					return err
				}
				if race == "uid" {
					if err := base.Delete(ctx, &replacement); err != nil {
						return err
					}
					replacement.UID, replacement.ResourceVersion = "", ""
					replacement.CreationTimestamp = metav1.Time{}
					return base.Create(ctx, &replacement)
				}
				replacement.Labels = map[string]string{"concurrent-update": "true"}
				return base.Update(ctx, &replacement)
			}}
			r.AccessPolicy.KCPCA = rotatedHistoryCA
			_, err := r.prepareAccess(ctx, inst, "history.example.test")
			r.Client = base
			if !apierrors.IsConflict(err) {
				t.Fatalf("API server did not reject stale deletion: %v", err)
			}
			var after corev1.Secret
			if err := base.Get(ctx, client.ObjectKeyFromObject(&replacement), &after); err != nil || after.UID != replacement.UID {
				t.Fatalf("deleted the concurrently modified/recreated Secret: %v", err)
			}
			if err := base.Get(ctx, client.ObjectKeyFromObject(inst), inst); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(pins, inst.Status.AccessSecretUIDs) {
				t.Fatal("failed API deletion removed a pin")
			}
		})
	}
}
