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

func TestEvaluateInstanceReadiness(t *testing.T) {
	t.Run("reports deployment progressing when status is stale", func(t *testing.T) {
		inst := &uiapiv1alpha1.ChatUIInstance{
			ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		}
		deploy := readyChatUIDeployment("demo-chatui", "default", 1)
		deploy.Status.ObservedGeneration = 0

		r := newChatUITestReconciler(nil, deploy)
		ready, reason, _, err := r.evaluateInstanceReadiness(context.Background(), inst, "demo-chatui")
		if err != nil {
			t.Fatalf("evaluateInstanceReadiness returned error: %v", err)
		}
		if ready {
			t.Fatalf("expected instance to be not ready")
		}
		if reason != "DeploymentProgressing" {
			t.Fatalf("expected reason DeploymentProgressing, got %q", reason)
		}
	})

	t.Run("reports missing endpoints when deployment is ready", func(t *testing.T) {
		inst := &uiapiv1alpha1.ChatUIInstance{
			ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		}
		deploy := readyChatUIDeployment("demo-chatui", "default", 1)

		r := newChatUITestReconciler(nil, deploy)
		ready, reason, _, err := r.evaluateInstanceReadiness(context.Background(), inst, "demo-chatui")
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
			ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		}
		deploy := readyChatUIDeployment("demo-chatui", "default", 1)
		endpoints := readyServiceEndpoints("demo-chatui", "default", 8080)

		r := newChatUITestReconciler(func(_ context.Context, _ string) error {
			return errors.New("probe failed")
		}, deploy, endpoints)

		ready, reason, _, err := r.evaluateInstanceReadiness(context.Background(), inst, "demo-chatui")
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
			ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		}
		deploy := readyChatUIDeployment("demo-chatui", "default", 1)
		endpoints := readyServiceEndpoints("demo-chatui", "default", 8080)

		r := newChatUITestReconciler(func(_ context.Context, _ string) error { return nil }, deploy, endpoints)

		ready, reason, _, err := r.evaluateInstanceReadiness(context.Background(), inst, "demo-chatui")
		if err != nil {
			t.Fatalf("evaluateInstanceReadiness returned error: %v", err)
		}
		if !ready {
			t.Fatalf("expected instance to be ready")
		}
		if reason != "Provisioned" {
			t.Fatalf("expected reason Provisioned, got %q", reason)
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
	if ensureChatUIContainerProbes(c) {
		t.Fatalf("expected second ensure call to report no changes")
	}
}

func newChatUITestReconciler(checker ServiceHealthChecker, objects ...client.Object) *ChatUIInstanceReconciler {
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
	}
}

func readyChatUIDeployment(name, namespace string, replicas int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  namespace,
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

func readyServiceEndpoints(name, namespace string, port int32) *corev1.Endpoints {
	return &corev1.Endpoints{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Subsets: []corev1.EndpointSubset{{
			Addresses: []corev1.EndpointAddress{{IP: "10.0.0.10"}},
			Ports:     []corev1.EndpointPort{{Port: port}},
		}},
	}
}
