/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	uiapi "github.com/apeirora/showroom-msp-chat-ui/api/v1alpha1"
	"github.com/apeirora/showroom-msp-chat-ui/internal/hostnamepolicy"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

var rejectedImages = []string{
	"ghcr.io/open-webui/open-webui:latest",
	"ghcr.io/open-webui/open-webui:v0.5.0",
	"ghcr.io/open-webui/open-webui@sha256:" + strings.Repeat("a", 64),
	"ghcr.io.evil/open-webui/open-webui@sha256:" + strings.Repeat("a", 64),
	"other.example/untrusted:latest",
	strings.Replace(uiapi.SupportedImage, "@", ":latest@", 1),
	" " + uiapi.SupportedImage,
	uiapi.SupportedImage + "\n",
	" ",
	strings.ToUpper(uiapi.SupportedImage),
}

func TestImagePolicyResolution(t *testing.T) {
	for _, requested := range []string{"", uiapi.SupportedImage} {
		got, err := resolveImage(requested)
		if err != nil || got != uiapi.SupportedImage {
			t.Fatalf("supported image resolution: image=%q error=%v", got, err)
		}
	}
	for _, requested := range rejectedImages {
		if _, err := resolveImage(requested); err == nil {
			t.Errorf("accepted unsupported image %q", requested)
		}
	}
}

type imagePolicyClient struct {
	client.Client
	secretReads        int
	failUpdate         bool
	deploymentUpdates  int
	failInstanceUpdate bool
}

func (c *imagePolicyClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*corev1.Secret); ok {
		c.secretReads++
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

func (c *imagePolicyClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if _, ok := obj.(*appsv1.Deployment); ok {
		c.deploymentUpdates++
		if c.failUpdate {
			return errors.New("synthetic deployment update failure")
		}
	}
	if _, ok := obj.(*uiapi.ChatUIInstance); ok && c.failInstanceUpdate {
		return errors.New("synthetic instance update failure")
	}
	return c.Client.Update(ctx, obj, opts...)
}

func imagePolicyFixture(t *testing.T, image string, existing bool) (*ChatUIInstanceReconciler, *uiapi.ChatUIInstance, *imagePolicyClient) {
	t.Helper()
	sch := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(sch); err != nil {
		t.Fatal(err)
	}
	if err := uiapi.AddToScheme(sch); err != nil {
		t.Fatal(err)
	}
	inst := &uiapi.ChatUIInstance{ObjectMeta: metav1.ObjectMeta{
		Name: "image-test", Namespace: "tenant", UID: "instance-uid", Generation: 3,
		CreationTimestamp: metav1.NewTime(time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)),
		Annotations:       map[string]string{slugAnnotationKey: "imagetest"},
		Finalizers:        []string{"ui.privatellms.msp/chatuiinstance-finalizer"},
	}, Spec: uiapi.ChatUIInstanceSpec{Image: image, Replicas: 1, CredentialsSecretRef: uiapi.CredentialsSecretReference{Name: "credentials"}}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "credentials", Namespace: inst.Namespace},
		Data: map[string][]byte{"OPENAI_API_KEY": []byte("synthetic-test-key"), "OPENAI_API_URL": []byte("https://example.test")}}
	objects := []client.Object{inst, secret}
	if existing {
		deployment := buildDeployment(inst, uiLabels(inst.Name), 1, "other.example/untrusted:latest", "previous")
		if err := ctrl.SetControllerReference(inst, &deployment, sch); err != nil {
			t.Fatal(err)
		}
		objects = append(objects, &deployment)
	}
	c := &imagePolicyClient{Client: fake.NewClientBuilder().WithScheme(sch).WithStatusSubresource(inst).WithObjects(objects...).Build()}
	p, err := hostnamepolicy.Parse(strings.NewReader(`{"cutover":"2026-10-26T00:00:00Z"}`))
	if err != nil {
		t.Fatal(err)
	}
	return &ChatUIInstanceReconciler{Client: c, Scheme: sch, HostnamePolicy: p, PublicHost: "example.test", PublicScheme: "https"}, inst, c
}

func TestImagePolicyRejectsBeforeCredentials(t *testing.T) {
	for _, existing := range []bool{false, true} {
		r, inst, c := imagePolicyFixture(t, "other.example/untrusted:latest", existing)
		ctx := context.Background()
		req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(inst)}
		if _, err := r.Reconcile(ctx, req); err != nil {
			t.Fatal(err)
		}
		if c.secretReads != 0 {
			var unsafeDeployment appsv1.Deployment
			_ = c.Get(ctx, client.ObjectKey{Namespace: inst.Namespace, Name: inst.Name + "-chatui"}, &unsafeDeployment)
			if len(unsafeDeployment.Spec.Template.Spec.Containers) > 0 {
				container := unsafeDeployment.Spec.Template.Spec.Containers[0]
				t.Fatalf("read credentials for rejected image: %d; deployed image=%q credential reference=%q", c.secretReads,
					container.Image, envSecretName(container.Env, "OPENAI_API_KEY"))
			}
			t.Fatalf("read credentials for rejected image: %d", c.secretReads)
		}
		if err := c.Get(ctx, req.NamespacedName, inst); err != nil {
			t.Fatal(err)
		}
		condition := meta.FindStatusCondition(inst.Status.Conditions, "Ready")
		if inst.Status.Phase != phaseError || inst.Status.ObservedGeneration != inst.Generation || condition == nil ||
			condition.Status != metav1.ConditionFalse || condition.Reason != "ImageNotAllowed" || condition.ObservedGeneration != inst.Generation {
			t.Fatalf("missing current rejection status: %+v", inst.Status)
		}
		var deployment appsv1.Deployment
		err := c.Get(ctx, client.ObjectKey{Namespace: inst.Namespace, Name: inst.Name + "-chatui"}, &deployment)
		if existing {
			if err != nil || deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 0 {
				t.Fatalf("old unapproved workload was not stopped: %v", err)
			}
			// Repeated reconciliation keeps the workload stopped.
			if _, err := r.Reconcile(ctx, req); err != nil {
				t.Fatal(err)
			}
			deployment.Spec.Replicas = ptrTo(int32(1))
			if err := c.Update(ctx, &deployment); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Reconcile(ctx, req); err != nil {
				t.Fatal(err)
			}
			if err := c.Get(ctx, client.ObjectKeyFromObject(&deployment), &deployment); err != nil || *deployment.Spec.Replicas != 0 {
				t.Fatal("outside scale-up left a rejected workload running")
			}
		} else if !apierrors.IsNotFound(err) {
			t.Fatalf("created a deployment for a rejected image: %v", err)
		}
	}
}

func TestImagePolicyRecoveryPinsDeployment(t *testing.T) {
	r, inst, c := imagePolicyFixture(t, "other.example/untrusted:latest", true)
	ctx := context.Background()
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(inst)}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, req.NamespacedName, inst); err != nil {
		t.Fatal(err)
	}
	inst.Spec.Image = ""
	if err := c.Update(ctx, inst); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	var deployment appsv1.Deployment
	if err := c.Get(ctx, client.ObjectKey{Namespace: inst.Namespace, Name: inst.Name + "-chatui"}, &deployment); err != nil {
		t.Fatal(err)
	}
	container := deployment.Spec.Template.Spec.Containers[0]
	if container.Image != uiapi.SupportedImage || *deployment.Spec.Replicas != 1 {
		t.Fatalf("supported workload not restored: image=%q replicas=%d", container.Image, *deployment.Spec.Replicas)
	}
	if envSecretName(container.Env, "OPENAI_API_KEY") != "credentials" {
		t.Fatal("supported workload lost its credential reference")
	}
}

func TestImagePolicyQuarantineFailure(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		r, inst, c := imagePolicyFixture(t, "other.example/untrusted:latest", true)
		ctx := context.Background()
		var deployment appsv1.Deployment
		key := client.ObjectKey{Namespace: inst.Namespace, Name: inst.Name + "-chatui"}
		if err := c.Get(ctx, key, &deployment); err != nil {
			t.Fatal(err)
		}
		if foreign {
			deployment.OwnerReferences[0].UID = "other-owner"
			if err := c.Update(ctx, &deployment); err != nil {
				t.Fatal(err)
			}
		} else {
			c.failUpdate = true
		}
		if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(inst)}); err == nil {
			t.Fatal("silently accepted a failed or unowned workload stop")
		}
		if err := c.Get(ctx, key, &deployment); err != nil || *deployment.Spec.Replicas != 1 {
			t.Fatal("modified foreign workload or ignored failed update")
		}
		if err := c.Get(ctx, client.ObjectKeyFromObject(inst), inst); err != nil {
			t.Fatal(err)
		}
		condition := meta.FindStatusCondition(inst.Status.Conditions, "Ready")
		if condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != "ImageNotAllowed" {
			t.Fatal("failed scale-down hid image rejection status")
		}
	}
}

func TestImagePolicySupportedSpecWithForeignDeployment(t *testing.T) {
	r, inst, c := imagePolicyFixture(t, "", true)
	ctx := context.Background()
	var deployment appsv1.Deployment
	key := client.ObjectKey{Namespace: inst.Namespace, Name: inst.Name + "-chatui"}
	if err := c.Get(ctx, key, &deployment); err != nil {
		t.Fatal(err)
	}
	deployment.OwnerReferences[0].UID = "another-instance"
	if err := c.Update(ctx, &deployment); err != nil {
		t.Fatal(err)
	}
	updates := c.deploymentUpdates
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(inst)}); !errors.Is(err, errDeploymentNotOwned) {
		t.Fatalf("expected ownership refusal, got %v", err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(inst), inst); err != nil {
		t.Fatal(err)
	}
	condition := meta.FindStatusCondition(inst.Status.Conditions, "Ready")
	if condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != "ImagePolicyError" ||
		condition.Message != "The named Deployment is not controlled by this ChatUIInstance" || c.secretReads != 0 || c.deploymentUpdates != updates {
		t.Fatal("foreign ownership did not fail with a safe status before resource access")
	}
	if err := c.Get(ctx, key, &deployment); err != nil || *deployment.Spec.Replicas != 1 {
		t.Fatal("modified a foreign Deployment")
	}
}

func TestImagePolicyTransientErrorStaysOutOfStatus(t *testing.T) {
	r, inst, c := imagePolicyFixture(t, "", true)
	c.failUpdate = true
	ctx := context.Background()
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(inst)}); err == nil {
		t.Fatal("ignored an API update error")
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(inst), inst); err != nil {
		t.Fatal(err)
	}
	if condition := meta.FindStatusCondition(inst.Status.Conditions, "Ready"); condition != nil || inst.Status.Phase == phaseError || c.secretReads != 0 {
		t.Fatal("published an internal API failure as an image policy error")
	}
}

func TestImagePolicyRejectsWithoutAddingFinalizer(t *testing.T) {
	r, inst, c := imagePolicyFixture(t, "other.example/untrusted:latest", false)
	ctx := context.Background()
	inst.Finalizers = nil
	if err := c.Update(ctx, inst); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(inst)}); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(inst), inst); err != nil || len(inst.Finalizers) != 0 || c.secretReads != 0 {
		t.Fatal("rejected new object reached finalizer writes or credential reads")
	}
}

func TestImagePolicyLegacyDeletionReportsFailure(t *testing.T) {
	r, inst, c := imagePolicyFixture(t, "other.example/untrusted:latest", false)
	ctx := context.Background()
	if err := c.Delete(ctx, inst); err != nil {
		t.Fatal(err)
	}
	c.failInstanceUpdate = true
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(inst)}); err == nil {
		t.Fatal("silently ignored finalizer removal failure")
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(inst), inst); err != nil || len(inst.Finalizers) != 1 {
		t.Fatal("lost a pending finalizer")
	}
}

func TestImagePolicyDefaultUpgradeStaysPinned(t *testing.T) {
	r, inst, c := imagePolicyFixture(t, "", true)
	ctx := context.Background()
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(inst)}
	var deployment appsv1.Deployment
	key := client.ObjectKey{Namespace: inst.Namespace, Name: inst.Name + "-chatui"}
	if err := c.Get(ctx, key, &deployment); err != nil {
		t.Fatal(err)
	}
	deployment.Spec.Template.Spec.Containers[0].Image = "ghcr.io/open-webui/open-webui:latest"
	if err := c.Update(ctx, &deployment); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, key, &deployment); err != nil || *deployment.Spec.Replicas != 0 || c.secretReads != 0 {
		t.Fatal("default image upgrade did not first scale down")
	}
	deployment.Status.ObservedGeneration = deployment.Generation
	deployment.Status.Replicas = 1
	if err := c.Status().Update(ctx, &deployment); err != nil {
		t.Fatal(err)
	}
	if result, err := r.Reconcile(ctx, req); err != nil || result.RequeueAfter == 0 || c.secretReads != 0 {
		t.Fatal("upgrade resumed while non-terminating old replicas remain")
	}
	deployment.Status.Replicas = 0
	if err := c.Status().Update(ctx, &deployment); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, key, &deployment); err != nil || *deployment.Spec.Replicas != 1 || deployment.Spec.Template.Spec.Containers[0].Image != uiapi.SupportedImage {
		t.Fatal("default image upgrade did not restore the supported workload")
	}
	updates := c.deploymentUpdates
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, key, &deployment); err != nil || *deployment.Spec.Replicas != 1 || c.deploymentUpdates != updates {
		t.Fatal("pinned workload was rewritten or scaled down again")
	}
}

func TestImagePolicyStopsOldImageBeforeMissingCredentials(t *testing.T) {
	r, inst, c := imagePolicyFixture(t, "", true)
	ctx := context.Background()
	if err := c.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "credentials", Namespace: inst.Namespace}}); err != nil {
		t.Fatal(err)
	}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(inst)}
	result, err := r.Reconcile(ctx, req)
	if err != nil || result.RequeueAfter == 0 || c.secretReads != 0 {
		t.Fatalf("image migration was gated by credentials: result=%v error=%v reads=%d", result, err, c.secretReads)
	}
	var deployment appsv1.Deployment
	key := client.ObjectKey{Namespace: inst.Namespace, Name: inst.Name + "-chatui"}
	if err := c.Get(ctx, key, &deployment); err != nil || *deployment.Spec.Replicas != 0 {
		t.Fatal("old image not stopped before credential lookup")
	}
	if err := c.Get(ctx, req.NamespacedName, inst); err != nil {
		t.Fatal(err)
	}
	condition := meta.FindStatusCondition(inst.Status.Conditions, "Ready")
	if condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != "ImageMigration" {
		t.Fatal("image migration left stale readiness")
	}
	// A stale observed generation must not allow replicas to be restored early.
	deployment.Generation = 2
	if err := c.Update(ctx, &deployment); err != nil {
		t.Fatal(err)
	}
	if result, err := r.Reconcile(ctx, req); err != nil || result.RequeueAfter == 0 || c.secretReads != 0 {
		t.Fatal("migration resumed before the stop was observed")
	}
}

func TestImagePolicyDoesNotBlockLegacyDeletion(t *testing.T) {
	r, inst, c := imagePolicyFixture(t, "other.example/untrusted:latest", false)
	ctx := context.Background()
	if err := c.Delete(ctx, inst); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(inst)}); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(inst), inst); !apierrors.IsNotFound(err) {
		t.Fatalf("image policy blocked legacy finalizer cleanup: %v", err)
	}
}

func TestImagePolicyAdmission(t *testing.T) {
	generated, err := os.ReadFile("../../config/crd/bases/ui.privatellms.msp_chatuiinstances.yaml")
	if err != nil {
		t.Fatal(err)
	}
	shipped, err := os.ReadFile("../../charts/chat-ui-operator/crds/ui.privatellms.msp_chatuiinstances.yaml")
	if err != nil || !bytes.Equal(generated, shipped) {
		t.Fatalf("shipped CRD differs from the admission-tested schema: %v", err)
	}
	e := &envtest.Environment{CRDDirectoryPaths: []string{"../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
	cfg, err := e.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := e.Stop(); err != nil {
			t.Error(err)
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
	ctx := context.Background()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "image-policy"}}); err != nil {
		t.Fatal(err)
	}
	withoutSecret := &uiapi.ChatUIInstance{ObjectMeta: metav1.ObjectMeta{GenerateName: "missing-secret-", Namespace: "image-policy"}}
	if err := c.Create(ctx, withoutSecret); !apierrors.IsInvalid(err) {
		t.Fatalf("regeneration weakened required Secret-name validation: %v", err)
	}
	for _, image := range append([]string{"", uiapi.SupportedImage}, rejectedImages...) {
		inst := &uiapi.ChatUIInstance{ObjectMeta: metav1.ObjectMeta{GenerateName: "image-", Namespace: "image-policy"},
			Spec: uiapi.ChatUIInstanceSpec{Image: image, CredentialsSecretRef: uiapi.CredentialsSecretReference{Name: "synthetic"}}}
		err := c.Create(ctx, inst)
		_, policyErr := resolveImage(image)
		if policyErr == nil && err != nil {
			t.Fatalf("admission rejected supported image: %v", err)
		}
		if policyErr != nil && !apierrors.IsInvalid(err) {
			t.Fatalf("admission accepted unsupported image %q: %v", image, err)
		}
		if err == nil {
			inst.Spec.Image = "other.example/untrusted:latest"
			if err := c.Update(ctx, inst); !apierrors.IsInvalid(err) {
				t.Fatalf("admission accepted an image replacement: %v", err)
			}
		}
	}
}
