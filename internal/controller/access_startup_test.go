package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestAccessProxyStartupAllowsWebUIInitialization(t *testing.T) {
	const healthPath = "/readyz"
	inst := hostnameInstance("ui", "tenant", "instance-uid")
	deployment := buildDeployment(inst, uiLabels(inst.Name), 1, "supported", "backend-checksum")
	applyAccessDeployment(&deployment, inst, &accessBundle{
		name: "access-config", revision: "access-revision", publicURL: "https://ui.example.test",
	}, "example.test/access-proxy:test")
	app := &deployment.Spec.Template.Spec.Containers[0]
	var gate *corev1.Container
	for i := range deployment.Spec.Template.Spec.Containers {
		container := &deployment.Spec.Template.Spec.Containers[i]
		if container.Name == "instance-access" {
			gate = container
			break
		}
	}
	if gate == nil || gate.StartupProbe == nil {
		t.Fatal("access proxy has no startup probe to protect WebUI initialization from liveness restarts")
	}
	if app.StartupProbe == nil || gate.StartupProbe.PeriodSeconds != app.StartupProbe.PeriodSeconds ||
		gate.StartupProbe.FailureThreshold != app.StartupProbe.FailureThreshold ||
		gate.StartupProbe.PeriodSeconds*gate.StartupProbe.FailureThreshold != 600 {
		t.Fatal("access proxy and WebUI do not share the ten-minute startup allowance")
	}
	for name, probe := range map[string]*corev1.Probe{
		"startup": gate.StartupProbe, "readiness": gate.ReadinessProbe, "liveness": gate.LivenessProbe,
	} {
		if probe == nil || probe.HTTPGet == nil || probe.HTTPGet.Path != healthPath ||
			probe.HTTPGet.Port.IntVal != 8083 || probe.HTTPGet.Scheme != corev1.URISchemeHTTP ||
			probe.SuccessThreshold != 1 {
			t.Fatalf("%s probe no longer checks the fixed upstream readiness endpoint", name)
		}
	}
	if gate.ReadinessProbe.PeriodSeconds != 5 || gate.ReadinessProbe.FailureThreshold != 6 ||
		gate.LivenessProbe.PeriodSeconds != 10 || gate.LivenessProbe.FailureThreshold != 6 {
		t.Fatal("startup allowance changed normal readiness or liveness timing")
	}
}
