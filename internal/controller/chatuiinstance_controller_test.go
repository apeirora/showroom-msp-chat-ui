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
	"context"
	"errors"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	uiapiv1alpha1 "github.com/example/chat-ui/api/v1alpha1"
)

const (
	testNamespace               = "default"
	testChatUIServiceName       = "demo-chatui"
	testInstanceHost            = "test.example.com"
	reasonDeploymentProgressing = "DeploymentProgressing"
	reasonProvisioned           = "Provisioned"
)

var noopDNSChecker DNSChecker = func(_ context.Context, _ string) error { return nil }

func TestEvaluateInstanceReadiness(t *testing.T) {
	t.Run("reports deployment progressing when status is stale", func(t *testing.T) {
		inst := &uiapiv1alpha1.ChatUIInstance{
			ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: testNamespace},
		}
		deploy := readyChatUIDeployment()
		deploy.Status.ObservedGeneration = 0

		r := newChatUITestReconciler(nil, noopDNSChecker, deploy)
		ready, reason, _, err := r.evaluateInstanceReadiness(context.Background(), inst, testChatUIServiceName, testInstanceHost)
		if err != nil {
			t.Fatalf("evaluateInstanceReadiness returned error: %v", err)
		}
		if ready {
			t.Fatalf("expected instance to be not ready")
		}
		if reason != reasonDeploymentProgressing {
			t.Fatalf("expected reason %s, got %q", reasonDeploymentProgressing, reason)
		}
	})

	t.Run("reports missing endpoints when deployment is ready", func(t *testing.T) {
		inst := &uiapiv1alpha1.ChatUIInstance{
			ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: testNamespace},
		}
		deploy := readyChatUIDeployment()

		r := newChatUITestReconciler(nil, noopDNSChecker, deploy)
		ready, reason, _, err := r.evaluateInstanceReadiness(context.Background(), inst, testChatUIServiceName, testInstanceHost)
		if err != nil {
			t.Fatalf("evaluateInstanceReadiness returned error: %v", err)
		}
		if ready {
			t.Fatalf("expected instance to be not ready")
		}
		if reason != "ServiceEndpointsMissing" {
			t.Fatalf("expected reason ServiceEndpointsMissing, got %q", reason)
		}
	})

	t.Run("reports health check failure when service probe fails", func(t *testing.T) {
		inst := &uiapiv1alpha1.ChatUIInstance{
			ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: testNamespace},
		}
		deploy := readyChatUIDeployment()
		endpoints := readyServiceEndpoints()

		r := newChatUITestReconciler(func(_ context.Context, _ string) error {
			return errors.New("probe failed")
		}, noopDNSChecker, deploy, endpoints)

		ready, reason, _, err := r.evaluateInstanceReadiness(context.Background(), inst, testChatUIServiceName, testInstanceHost)
		if err != nil {
			t.Fatalf("evaluateInstanceReadiness returned error: %v", err)
		}
		if ready {
			t.Fatalf("expected instance to be not ready")
		}
		if reason != "HealthCheckFailed" {
			t.Fatalf("expected reason HealthCheckFailed, got %q", reason)
		}
	})

	t.Run("marks ready when deployment, endpoints, and probe are healthy", func(t *testing.T) {
		inst := &uiapiv1alpha1.ChatUIInstance{
			ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: testNamespace},
		}
		deploy := readyChatUIDeployment()
		endpoints := readyServiceEndpoints()

		r := newChatUITestReconciler(func(_ context.Context, _ string) error { return nil }, noopDNSChecker, deploy, endpoints)

		ready, reason, _, err := r.evaluateInstanceReadiness(context.Background(), inst, testChatUIServiceName, testInstanceHost)
		if err != nil {
			t.Fatalf("evaluateInstanceReadiness returned error: %v", err)
		}
		if !ready {
			t.Fatalf("expected instance to be ready")
		}
		if reason != reasonProvisioned {
			t.Fatalf("expected reason %s, got %q", reasonProvisioned, reason)
		}
	})

	t.Run("reports DNS not ready when lookup fails", func(t *testing.T) {
		inst := &uiapiv1alpha1.ChatUIInstance{
			ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: testNamespace},
		}
		deploy := readyChatUIDeployment()
		endpoints := readyServiceEndpoints()

		failingDNS := DNSChecker(func(_ context.Context, _ string) error {
			return errors.New("no such host")
		})
		r := newChatUITestReconciler(
			func(_ context.Context, _ string) error { return nil },
			failingDNS, deploy, endpoints,
		)

		ready, reason, _, err := r.evaluateInstanceReadiness(context.Background(), inst, testChatUIServiceName, testInstanceHost)
		if err != nil {
			t.Fatalf("evaluateInstanceReadiness returned error: %v", err)
		}
		if ready {
			t.Fatalf("expected instance to be not ready")
		}
		if reason != "DNSNotReady" {
			t.Fatalf("expected reason DNSNotReady, got %q", reason)
		}
	})

	t.Run("marks ready when all checks including DNS pass", func(t *testing.T) {
		inst := &uiapiv1alpha1.ChatUIInstance{
			ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: testNamespace},
		}
		deploy := readyChatUIDeployment()
		endpoints := readyServiceEndpoints()

		passingDNS := DNSChecker(func(_ context.Context, _ string) error { return nil })
		r := newChatUITestReconciler(
			func(_ context.Context, _ string) error { return nil },
			passingDNS, deploy, endpoints,
		)

		ready, reason, _, err := r.evaluateInstanceReadiness(context.Background(), inst, testChatUIServiceName, testInstanceHost)
		if err != nil {
			t.Fatalf("evaluateInstanceReadiness returned error: %v", err)
		}
		if !ready {
			t.Fatalf("expected instance to be ready")
		}
		if reason != reasonProvisioned {
			t.Fatalf("expected reason %s, got %q", reasonProvisioned, reason)
		}
	})
}

func TestEnsureChatUIContainerProbes(t *testing.T) {
	c := &corev1.Container{Name: "open-webui"}
	if !ensureChatUIContainerProbes(c) {
		t.Fatalf("expected probe configuration to be applied")
	}
	if c.ReadinessProbe == nil || c.ReadinessProbe.HTTPGet == nil || c.ReadinessProbe.HTTPGet.Path != "/" {
		t.Fatalf("readiness probe was not configured correctly")
	}
	if c.LivenessProbe == nil || c.LivenessProbe.HTTPGet == nil || c.LivenessProbe.HTTPGet.Path != "/" {
		t.Fatalf("liveness probe was not configured correctly")
	}
	if c.StartupProbe == nil || c.StartupProbe.HTTPGet == nil || c.StartupProbe.HTTPGet.Path != "/" {
		t.Fatalf("startup probe was not configured correctly")
	}
	if c.StartupProbe.FailureThreshold != 60 {
		t.Fatalf("expected startup probe to allow slow first boot, got failure threshold %d", c.StartupProbe.FailureThreshold)
	}
	if ensureChatUIContainerProbes(c) {
		t.Fatalf("expected second ensure call to report no changes")
	}
	c.LivenessProbe.InitialDelaySeconds = 1
	if !ensureChatUIContainerProbes(c) {
		t.Fatalf("expected drifted probe timing to be corrected")
	}
	if c.LivenessProbe.InitialDelaySeconds != 20 {
		t.Fatalf("expected liveness initial delay to be restored, got %d", c.LivenessProbe.InitialDelaySeconds)
	}
}

func TestReconcileDeploymentUpdatesManagedOpenWebUIEnv(t *testing.T) {
	inst := &uiapiv1alpha1.ChatUIInstance{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: testNamespace, UID: "test-uid"},
		Spec: uiapiv1alpha1.ChatUIInstanceSpec{
			CredentialsSecretRef: corev1.LocalObjectReference{Name: "new-token"},
			Replicas:             1,
		},
	}
	replicas := int32(1)
	existing := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-chatui", Namespace: testNamespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "open-webui",
						Image: "ghcr.io/open-webui/open-webui:latest",
						Env: []corev1.EnvVar{
							{Name: "WEBUI_AUTH", Value: "true"},
							{Name: "WEBUI_ADMIN_EMAIL", Value: "admin@localhost"},
							{
								Name: "OPENAI_API_KEY",
								ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{Name: "old-token"},
									Key:                  "OPENAI_API_KEY",
								}},
							},
						},
					}},
				},
			},
		},
	}
	r := newChatUITestReconciler(nil, noopDNSChecker, existing)

	if err := r.reconcileDeployment(context.Background(), inst, uiLabels(inst.Name), 1, "ghcr.io/open-webui/open-webui:latest", "checksum"); err != nil {
		t.Fatalf("reconcileDeployment returned error: %v", err)
	}

	var updated appsv1.Deployment
	if err := r.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: "demo-chatui"}, &updated); err != nil {
		t.Fatalf("failed to get updated deployment: %v", err)
	}
	env := updated.Spec.Template.Spec.Containers[0].Env
	if got := envValue(env, "WEBUI_AUTH"); got != "false" {
		t.Fatalf("expected WEBUI_AUTH=false, got %q", got)
	}
	if got := envValue(env, "WEBUI_ADMIN_EMAIL"); got != "" {
		t.Fatalf("expected stale WEBUI_ADMIN_EMAIL to be removed, got %q", got)
	}
	keyRef := envSecretName(env, "OPENAI_API_KEY")
	if keyRef != "new-token" {
		t.Fatalf("expected OPENAI_API_KEY to reference new-token, got %q", keyRef)
	}
}

func newChatUITestReconciler(checker ServiceHealthChecker, dnsChecker DNSChecker, objects ...client.Object) *ChatUIInstanceReconciler {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = uiapiv1alpha1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		Build()

	return &ChatUIInstanceReconciler{
		Client:               fakeClient,
		Scheme:               scheme,
		ServiceHealthChecker: checker,
		DNSChecker:           dnsChecker,
	}
}

func envValue(env []corev1.EnvVar, name string) string {
	for _, item := range env {
		if item.Name == name {
			return item.Value
		}
	}
	return ""
}

func envSecretName(env []corev1.EnvVar, name string) string {
	for _, item := range env {
		if item.Name == name && item.ValueFrom != nil && item.ValueFrom.SecretKeyRef != nil {
			return item.ValueFrom.SecretKeyRef.Name
		}
	}
	return ""
}

func readyChatUIDeployment() *appsv1.Deployment {
	replicas := int32(1)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:       testChatUIServiceName,
			Namespace:  testNamespace,
			Generation: 1,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
		},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 1,
			UpdatedReplicas:    replicas,
			ReadyReplicas:      replicas,
			AvailableReplicas:  replicas,
		},
	}
}

func readyServiceEndpoints() *corev1.Endpoints {
	return &corev1.Endpoints{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testChatUIServiceName,
			Namespace: testNamespace,
		},
		Subsets: []corev1.EndpointSubset{{
			Addresses: []corev1.EndpointAddress{{IP: "10.0.0.10"}},
			Ports:     []corev1.EndpointPort{{Port: 8080}},
		}},
	}
}
