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
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	intstr "k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"

	uiapiv1alpha1 "github.com/example/chat-ui/api/v1alpha1"
)

// ChatUIInstanceReconciler reconciles a ChatUIInstance object
type ChatUIInstanceReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// Public host/scheme for ingress and status.url
	PublicHost   string
	PublicScheme string

	// ExtraIngressAnnotations are merged into managed Ingress annotations.
	ExtraIngressAnnotations map[string]string
	// TLSSecretName, when non-empty, configures spec.tls with this secret for PublicHost.
	TLSSecretName string
	// ServiceHealthChecker probes service endpoints before publishing Ready=True.
	// If nil, a default HTTP checker is used.
	ServiceHealthChecker ServiceHealthChecker
	// DNSChecker verifies that the instance hostname resolves before publishing Ready=True.
	// If nil, a default net.Resolver-based checker is used.
	DNSChecker DNSChecker
}

const (
	slugAnnotationKey        = "ui.privatellms.msp/slug"
	secretChecksumAnnotation = "ui.privatellms.msp/secret-checksum"
	slugAlphabet             = "abcdefghijklmnopqrstuvwxyz0123456789"
	provisioningRequeueAfter = 5 * time.Second
)

type ServiceHealthChecker func(ctx context.Context, targetURL string) error

type DNSChecker func(ctx context.Context, host string) error

//+kubebuilder:rbac:groups=ui.privatellms.msp,resources=chatuiinstances,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=ui.privatellms.msp,resources=chatuiinstances/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=ui.privatellms.msp,resources=chatuiinstances/finalizers,verbs=update
// core + apps
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=endpoints,verbs=get;list;watch
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete

func (r *ChatUIInstanceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx, span := r.startTracing(ctx)
	defer span.End()
	logger := log.FromContext(ctx)

	inst, found, err := r.getInstance(ctx, req.NamespacedName)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !found {
		return ctrl.Result{}, nil
	}

	// Deletion handling: remove finalizer without blocking
	const finalizerName = "ui.privatellms.msp/chatuiinstance-finalizer"
	if !inst.DeletionTimestamp.IsZero() {
		if ctrlutil.ContainsFinalizer(inst, finalizerName) {
			ctrlutil.RemoveFinalizer(inst, finalizerName)
			_ = r.Update(ctx, inst)
		}
		return ctrl.Result{}, nil
	}

	// Ensure finalizer
	if !ctrlutil.ContainsFinalizer(inst, finalizerName) {
		ctrlutil.AddFinalizer(inst, finalizerName)
		if err := r.Update(ctx, inst); err != nil {
			return ctrl.Result{RequeueAfter: 3 * time.Second}, nil
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Validate secret reference
	secretName := strings.TrimSpace(inst.Spec.CredentialsSecretRef.Name)
	if secretName == "" {
		meta.SetStatusCondition(&inst.Status.Conditions, metav1.Condition{
			Type: "Ready", Status: metav1.ConditionFalse, Reason: "MissingSecret",
			Message:            "spec.credentialsSecretRef.name must point to a Secret with OPENAI_API_URL and OPENAI_API_KEY",
			LastTransitionTime: metav1.NewTime(time.Now()),
		})
		inst.Status.Phase = "Error"
		_ = r.Status().Update(ctx, inst)
		logger.Info("backend secret name is not set")
		return ctrl.Result{}, nil
	}

	// Fetch the credentials secret and compute checksum for rollout detection
	var credentialsSecret corev1.Secret
	secretKey := client.ObjectKey{Namespace: inst.Namespace, Name: secretName}
	if err := r.Get(ctx, secretKey, &credentialsSecret); err != nil {
		if apierrors.IsNotFound(err) {
			meta.SetStatusCondition(&inst.Status.Conditions, metav1.Condition{
				Type: "Ready", Status: metav1.ConditionFalse, Reason: "SecretNotFound",
				Message:            fmt.Sprintf("Secret %q not found", secretName),
				LastTransitionTime: metav1.NewTime(time.Now()),
			})
			inst.Status.Phase = "Error"
			_ = r.Status().Update(ctx, inst)
			logger.Info("credentials secret not found", "secret", secretName)
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		return ctrl.Result{}, err
	}
	secretChecksum := computeSecretChecksum(&credentialsSecret)

	// Ensure slug
	slug, requeue, err := r.ensureSlug(ctx, inst)
	if err != nil {
		return ctrl.Result{}, err
	}
	if requeue {
		return ctrl.Result{Requeue: true}, nil
	}

	labels := uiLabels(inst.Name)
	replicas := inst.Spec.Replicas
	if replicas <= 0 {
		replicas = 1
	}
	image := strings.TrimSpace(inst.Spec.Image)
	if image == "" {
		image = "ghcr.io/open-webui/open-webui:latest"
	}

	// Reconcile Deployment
	if err := r.reconcileDeployment(ctx, inst, labels, replicas, image, secretChecksum); err != nil {
		return ctrl.Result{}, err
	}

	// Reconcile Service
	svcName := fmt.Sprintf("%s-chatui", inst.Name)
	if err := r.reconcileService(ctx, inst, labels, svcName); err != nil {
		return ctrl.Result{}, err
	}

	instanceHost, err := r.instanceHost(slug)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Reconcile Ingress
	if err := r.reconcileIngress(ctx, inst, labels, svcName, instanceHost, "/"); err != nil {
		return ctrl.Result{}, err
	}

	ready, reason, message, err := r.evaluateInstanceReadiness(ctx, inst, svcName, instanceHost)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !ready {
		if err := r.updateProvisioningStatus(ctx, inst, instanceHost, reason, message); err != nil {
			return ctrl.Result{}, err
		}
		logger.Info("ChatUIInstance is still provisioning", "name", req.NamespacedName, "reason", reason)
		return ctrl.Result{RequeueAfter: provisioningRequeueAfter}, nil
	}

	// Update status to Ready only after health checks pass
	if err := r.updateReadyStatus(ctx, inst, instanceHost); err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("reconciled ChatUIInstance", "name", req.NamespacedName)
	return ctrl.Result{}, nil
}

func (r *ChatUIInstanceReconciler) startTracing(ctx context.Context) (context.Context, trace.Span) {
	tracer := otel.Tracer("github.com/example/chat-ui/internal/controller")
	ctx, span := tracer.Start(ctx, "ChatUIInstanceReconciler.Reconcile", trace.WithAttributes())
	logger := log.FromContext(ctx)
	if sc := span.SpanContext(); sc.IsValid() {
		logger = logger.WithValues(
			"trace_id", sc.TraceID().String(),
			"span_id", sc.SpanID().String(),
		)
	}
	ctx = log.IntoContext(ctx, logger)
	return ctx, span
}

func (r *ChatUIInstanceReconciler) getInstance(ctx context.Context, name types.NamespacedName) (*uiapiv1alpha1.ChatUIInstance, bool, error) {
	inst := &uiapiv1alpha1.ChatUIInstance{}
	if err := r.Get(ctx, name, inst); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return inst, true, nil
}

func (r *ChatUIInstanceReconciler) ensureSlug(ctx context.Context, inst *uiapiv1alpha1.ChatUIInstance) (string, bool, error) {
	anns := inst.GetAnnotations()
	slug := ""
	if anns != nil {
		slug = strings.TrimSpace(anns[slugAnnotationKey])
	}
	if slug != "" && isValidSlug(slug) {
		return slug, false, nil
	}
	newSlug, err := generateSlug(12)
	if err != nil {
		return "", false, err
	}
	if anns == nil {
		anns = map[string]string{}
	}
	anns[slugAnnotationKey] = newSlug
	inst.SetAnnotations(anns)
	if err := r.Update(ctx, inst); err != nil {
		return "", false, err
	}
	return newSlug, true, nil
}

func uiLabels(instanceName string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":      "open-webui",
		"ui.privatellms.msp/instance": instanceName,
	}
}

func (r *ChatUIInstanceReconciler) reconcileDeployment(ctx context.Context, inst *uiapiv1alpha1.ChatUIInstance, labels map[string]string, replicas int32, image string, secretChecksum string) error {
	logger := log.FromContext(ctx)
	deployName := fmt.Sprintf("%s-chatui", inst.Name)

	var existing appsv1.Deployment
	key := client.ObjectKey{Namespace: inst.Namespace, Name: deployName}
	if err := r.Get(ctx, key, &existing); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		deploy := buildDeployment(inst, labels, replicas, image, secretChecksum)
		if err := ctrl.SetControllerReference(inst, &deploy, r.Scheme); err != nil {
			return err
		}
		if err := r.Create(ctx, &deploy); err != nil {
			return err
		}
		logger.Info("created Chat UI Deployment", "name", deployName)
		return nil
	}
	updated := false

	// Check if checksum annotation needs update (triggers pod rollout)
	currentChecksum := ""
	if existing.Spec.Template.Annotations != nil {
		currentChecksum = existing.Spec.Template.Annotations[secretChecksumAnnotation]
	}
	if currentChecksum != secretChecksum {
		if existing.Spec.Template.Annotations == nil {
			existing.Spec.Template.Annotations = map[string]string{}
		}
		existing.Spec.Template.Annotations[secretChecksumAnnotation] = secretChecksum
		updated = true
		logger.Info("secret checksum changed, triggering rollout",
			"deployment", deployName,
			"oldChecksum", currentChecksum,
			"newChecksum", secretChecksum)
	}

	if existing.Spec.Replicas == nil || *existing.Spec.Replicas != replicas {
		existing.Spec.Replicas = ptrTo(replicas)
		updated = true
	}
	desiredSecretName := strings.TrimSpace(inst.Spec.CredentialsSecretRef.Name)
	for i := range existing.Spec.Template.Spec.Containers {
		c := &existing.Spec.Template.Spec.Containers[i]
		if c.Name != "open-webui" {
			continue
		}
		if c.Image != image {
			c.Image = image
			updated = true
		}
		if ensureChatUIContainerProbes(c) {
			updated = true
		}
		// Update secret references if credentialsSecretRef.name changed
		for j := range c.Env {
			env := &c.Env[j]
			if env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil {
				if env.ValueFrom.SecretKeyRef.Name != desiredSecretName {
					logger.Info("updating secret reference in env var",
						"envVar", env.Name,
						"oldSecret", env.ValueFrom.SecretKeyRef.Name,
						"newSecret", desiredSecretName)
					env.ValueFrom.SecretKeyRef.Name = desiredSecretName
					updated = true
				}
			}
		}
	}
	if !updated {
		return nil
	}
	return r.Update(ctx, &existing)
}

func buildDeployment(inst *uiapiv1alpha1.ChatUIInstance, labels map[string]string, replicas int32, image string, secretChecksum string) appsv1.Deployment {
	secretName := inst.Spec.CredentialsSecretRef.Name
	// Derive a stable 32-byte WEBUI_SECRET_KEY from the instance UID so JWT
	// sessions survive pod restarts. Upstream Open WebUI generates an
	// ephemeral 16-byte key when the env var is empty and logs an
	// InsecureKeyLengthWarning.
	webUIKeyDigest := sha256.Sum256([]byte("ui.privatellms.msp/webui-secret-key:" + string(inst.UID)))
	webUISecretKey := hex.EncodeToString(webUIKeyDigest[:])
	envVars := []corev1.EnvVar{
		// Core auth + connector settings
		{Name: "WEBUI_AUTH", Value: "false"},
		{Name: "WEBUI_SECRET_KEY", Value: webUISecretKey},
		{Name: "ENABLE_OPENAI_API", Value: "true"},
		{Name: "ENABLE_OLLAMA_API", Value: "false"},
		{Name: "ENABLE_DIRECT_CONNECTIONS", Value: "false"},

		// Access & admin experience
		{Name: "ENABLE_LOGIN_FORM", Value: "false"},
		{Name: "ENABLE_SIGNUP", Value: "false"},
		{Name: "ENABLE_OAUTH_SIGNUP", Value: "false"},
		{Name: "ENABLE_SIGNUP_PASSWORD_CONFIRMATION", Value: "false"},
		{Name: "ENABLE_ADMIN_EXPORT", Value: "false"},
		{Name: "ENABLE_ADMIN_CHAT_ACCESS", Value: "false"},
		{Name: "SHOW_ADMIN_DETAILS", Value: "false"},

		// Section toggles for a minimal UI
		{Name: "ENABLE_CHANNELS", Value: "false"},
		{Name: "ENABLE_NOTES", Value: "false"},
		{Name: "ENABLE_COMMUNITY_SHARING", Value: "false"},
		{Name: "ENABLE_MESSAGE_RATING", Value: "false"},
		{Name: "ENABLE_USER_WEBHOOKS", Value: "false"},
		{Name: "ENABLE_EVALUATION_ARENA_MODELS", Value: "false"},
		{Name: "ENABLE_API_KEYS", Value: "false"},
		{Name: "ENABLE_API_KEYS_ENDPOINT_RESTRICTIONS", Value: "false"},
		{Name: "ENABLE_VERSION_UPDATE_CHECK", Value: "false"},

		// LLM-side bells & whistles
		{Name: "ENABLE_WEB_SEARCH", Value: "false"},
		{Name: "ENABLE_RAG_HYBRID_SEARCH", Value: "false"},
		{Name: "ENABLE_RAG_HYBRID_SEARCH_ENRICHED_TEXTS", Value: "false"},
		{Name: "ENABLE_RAG_LOCAL_WEB_FETCH", Value: "false"},
		{Name: "ENABLE_GOOGLE_DRIVE_INTEGRATION", Value: "false"},
		{Name: "ENABLE_ONEDRIVE_INTEGRATION", Value: "false"},
		{Name: "ENABLE_ONEDRIVE_PERSONAL", Value: "false"},
		{Name: "ENABLE_ONEDRIVE_BUSINESS", Value: "false"},
		{Name: "ENABLE_CODE_EXECUTION", Value: "false"},
		{Name: "ENABLE_CODE_INTERPRETER", Value: "false"},
		{Name: "ENABLE_IMAGE_GENERATION", Value: "false"},
		{Name: "ENABLE_IMAGE_PROMPT_GENERATION", Value: "false"},
		{Name: "ENABLE_IMAGE_EDIT", Value: "false"},

		// Workspace navigation / sharing permissions
		{Name: "USER_PERMISSIONS_WORKSPACE_MODELS_ACCESS", Value: "false"},
		{Name: "USER_PERMISSIONS_WORKSPACE_KNOWLEDGE_ACCESS", Value: "false"},
		{Name: "USER_PERMISSIONS_WORKSPACE_PROMPTS_ACCESS", Value: "false"},
		{Name: "USER_PERMISSIONS_WORKSPACE_TOOLS_ACCESS", Value: "false"},
		{Name: "USER_PERMISSIONS_WORKSPACE_MODELS_IMPORT", Value: "false"},
		{Name: "USER_PERMISSIONS_WORKSPACE_MODELS_EXPORT", Value: "false"},
		{Name: "USER_PERMISSIONS_WORKSPACE_PROMPTS_IMPORT", Value: "false"},
		{Name: "USER_PERMISSIONS_WORKSPACE_PROMPTS_EXPORT", Value: "false"},
		{Name: "USER_PERMISSIONS_WORKSPACE_TOOLS_IMPORT", Value: "false"},
		{Name: "USER_PERMISSIONS_WORKSPACE_TOOLS_EXPORT", Value: "false"},
		{Name: "USER_PERMISSIONS_WORKSPACE_MODELS_ALLOW_SHARING", Value: "false"},
		{Name: "USER_PERMISSIONS_WORKSPACE_MODELS_ALLOW_PUBLIC_SHARING", Value: "false"},
		{Name: "USER_PERMISSIONS_WORKSPACE_KNOWLEDGE_ALLOW_SHARING", Value: "false"},
		{Name: "USER_PERMISSIONS_WORKSPACE_KNOWLEDGE_ALLOW_PUBLIC_SHARING", Value: "false"},
		{Name: "USER_PERMISSIONS_WORKSPACE_PROMPTS_ALLOW_SHARING", Value: "false"},
		{Name: "USER_PERMISSIONS_WORKSPACE_PROMPTS_ALLOW_PUBLIC_SHARING", Value: "false"},
		{Name: "USER_PERMISSIONS_WORKSPACE_TOOLS_ALLOW_SHARING", Value: "false"},
		{Name: "USER_PERMISSIONS_WORKSPACE_TOOLS_ALLOW_PUBLIC_SHARING", Value: "false"},
		{Name: "USER_PERMISSIONS_NOTES_ALLOW_SHARING", Value: "false"},
		{Name: "USER_PERMISSIONS_NOTES_ALLOW_PUBLIC_SHARING", Value: "false"},

		// Chat level feature flags
		{Name: "USER_PERMISSIONS_CHAT_CONTROLS", Value: "false"},
		{Name: "USER_PERMISSIONS_CHAT_VALVES", Value: "false"},
		{Name: "USER_PERMISSIONS_CHAT_SYSTEM_PROMPT", Value: "false"},
		{Name: "USER_PERMISSIONS_CHAT_PARAMS", Value: "false"},
		{Name: "USER_PERMISSIONS_CHAT_FILE_UPLOAD", Value: "false"},
		{Name: "USER_PERMISSIONS_CHAT_SHARE", Value: "false"},
		{Name: "USER_PERMISSIONS_CHAT_EXPORT", Value: "false"},
		{Name: "USER_PERMISSIONS_CHAT_STT", Value: "false"},
		{Name: "USER_PERMISSIONS_CHAT_TTS", Value: "false"},
		{Name: "USER_PERMISSIONS_CHAT_CALL", Value: "false"},
		{Name: "USER_PERMISSIONS_CHAT_MULTIPLE_MODELS", Value: "false"},
		{Name: "USER_PERMISSIONS_CHAT_TEMPORARY", Value: "false"},
		{Name: "USER_PERMISSIONS_CHAT_RATE_RESPONSE", Value: "false"},

		// Feature toggles surfaced in permissions
		{Name: "USER_PERMISSIONS_FEATURES_DIRECT_TOOL_SERVERS", Value: "false"},
		{Name: "USER_PERMISSIONS_FEATURES_WEB_SEARCH", Value: "false"},
		{Name: "USER_PERMISSIONS_FEATURES_IMAGE_GENERATION", Value: "false"},
		{Name: "USER_PERMISSIONS_FEATURES_CODE_INTERPRETER", Value: "false"},
		{Name: "USER_PERMISSIONS_FEATURES_NOTES", Value: "false"},
		{Name: "USER_PERMISSIONS_FEATURES_API_KEYS", Value: "false"},
	}
	envVars = append(envVars,
		corev1.EnvVar{
			Name: "OPENAI_API_KEY",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
					Key:                  "OPENAI_API_KEY",
					Optional:             ptrTo(false),
				},
			},
		},
		corev1.EnvVar{
			Name: "OPENAI_API_BASE_URL",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
					Key:                  "OPENAI_API_URL",
					Optional:             ptrTo(false),
				},
			},
		},
	)
	container := corev1.Container{
		Name:  "open-webui",
		Image: image,
		Env:   envVars,
		Ports: []corev1.ContainerPort{{ContainerPort: 8080, Protocol: corev1.ProtocolTCP}},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/",
					Port: intstr.FromInt(8080),
				},
			},
			PeriodSeconds:    5,
			TimeoutSeconds:   2,
			FailureThreshold: 6,
		},
		StartupProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/",
					Port: intstr.FromInt(8080),
				},
			},
			PeriodSeconds:    10,
			TimeoutSeconds:   2,
			FailureThreshold: 60,
		},
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/",
					Port: intstr.FromInt(8080),
				},
			},
			InitialDelaySeconds: 20,
			PeriodSeconds:       10,
			TimeoutSeconds:      2,
			FailureThreshold:    6,
		},
	}
	return appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-chatui", inst.Name),
			Namespace: inst.Namespace,
			Labels:    copyStringMap(labels),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: copyStringMap(labels)},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: copyStringMap(labels),
					Annotations: map[string]string{
						secretChecksumAnnotation: secretChecksum,
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{container},
				},
			},
		},
	}
}

func (r *ChatUIInstanceReconciler) reconcileService(ctx context.Context, inst *uiapiv1alpha1.ChatUIInstance, labels map[string]string, svcName string) error {
	logger := log.FromContext(ctx)
	var svc corev1.Service
	key := client.ObjectKey{Namespace: inst.Namespace, Name: svcName}
	if err := r.Get(ctx, key, &svc); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		service := corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      svcName,
				Namespace: inst.Namespace,
				Labels:    copyStringMap(labels),
			},
			Spec: corev1.ServiceSpec{
				Selector: copyStringMap(labels),
				Ports: []corev1.ServicePort{{
					Name:       "http",
					Port:       8080,
					TargetPort: intstr.FromInt(8080),
					Protocol:   corev1.ProtocolTCP,
				}},
			},
		}
		if err := ctrl.SetControllerReference(inst, &service, r.Scheme); err != nil {
			return err
		}
		if err := r.Create(ctx, &service); err != nil {
			return err
		}
		logger.Info("created Chat UI Service", "name", svcName)
	}
	return nil
}

func (r *ChatUIInstanceReconciler) reconcileIngress(ctx context.Context, inst *uiapiv1alpha1.ChatUIInstance, labels map[string]string, svcName, host, pathPrefix string) error {
	logger := log.FromContext(ctx)
	ingressName := svcName
	var ing networkingv1.Ingress
	key := client.ObjectKey{Namespace: inst.Namespace, Name: ingressName}
	if err := r.Get(ctx, key, &ing); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		newIngress := r.buildDesiredIngress(inst, labels, svcName, host, pathPrefix)
		if err := ctrl.SetControllerReference(inst, newIngress, r.Scheme); err != nil {
			return err
		}
		if err := r.Create(ctx, newIngress); err != nil {
			return err
		}
		logger.Info("created Ingress for Chat UI", "name", ingressName)
		return nil
	}
	isHTTPS := strings.EqualFold(r.PublicScheme, "https")
	desiredEntry := r.desiredIngressEntryPoints(isHTTPS)

	updated := r.ensureIngressAnnotations(&ing, desiredEntry, isHTTPS)
	if r.applyExtraIngressAnnotations(&ing, true) {
		updated = true
	}
	if r.ensureIngressDNSAnnotation(&ing, host) {
		updated = true
	}
	if r.ensureIngressClass(&ing) {
		updated = true
	}
	if r.ensureIngressHTTPRule(&ing, host, pathPrefix, svcName) {
		updated = true
	}
	if r.ensureIngressTLS(&ing, host, isHTTPS) {
		updated = true
	}
	if !updated {
		return nil
	}
	return r.Update(ctx, &ing)
}

func (r *ChatUIInstanceReconciler) buildDesiredIngress(inst *uiapiv1alpha1.ChatUIInstance, labels map[string]string, svcName, host, pathPrefix string) *networkingv1.Ingress {
	className := "traefik"
	pathType := networkingv1.PathTypePrefix
	isHTTPS := strings.EqualFold(r.PublicScheme, "https")
	desiredEntry := r.desiredIngressEntryPoints(isHTTPS)
	ingress := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      svcName,
			Namespace: inst.Namespace,
			Labels:    copyStringMap(labels),
			Annotations: map[string]string{
				"traefik.ingress.kubernetes.io/router.entrypoints": desiredEntry,
			},
		},
		Spec: networkingv1.IngressSpec{
			IngressClassName: &className,
			Rules: []networkingv1.IngressRule{{
				Host: host,
				IngressRuleValue: networkingv1.IngressRuleValue{
					HTTP: &networkingv1.HTTPIngressRuleValue{
						Paths: []networkingv1.HTTPIngressPath{{
							Path:     pathPrefix,
							PathType: &pathType,
							Backend: networkingv1.IngressBackend{
								Service: &networkingv1.IngressServiceBackend{
									Name: svcName,
									Port: networkingv1.ServiceBackendPort{Number: 8080},
								},
							},
						}},
					},
				},
			}},
			TLS: func() []networkingv1.IngressTLS {
				if !isHTTPS {
					return nil
				}
				desiredTLS := strings.TrimSpace(r.TLSSecretName)
				if desiredTLS == "" {
					return nil
				}
				return []networkingv1.IngressTLS{{
					Hosts:      []string{host},
					SecretName: desiredTLS,
				}}
			}(),
		},
	}
	r.applyExtraIngressAnnotations(ingress, false)
	r.ensureIngressDNSAnnotation(ingress, host)
	return ingress
}

func (r *ChatUIInstanceReconciler) desiredIngressEntryPoints(isHTTPS bool) string {
	if isHTTPS {
		return "websecure,web"
	}
	return "web"
}

func (r *ChatUIInstanceReconciler) ensureIngressAnnotations(ing *networkingv1.Ingress, desiredEntry string, isHTTPS bool) bool {
	updated := false
	if ing.Annotations == nil {
		ing.Annotations = map[string]string{}
	}
	if ing.Annotations["traefik.ingress.kubernetes.io/router.entrypoints"] != desiredEntry {
		ing.Annotations["traefik.ingress.kubernetes.io/router.entrypoints"] = desiredEntry
		updated = true
	}
	if isHTTPS {
		if ing.Annotations["traefik.ingress.kubernetes.io/router.tls"] != "true" {
			ing.Annotations["traefik.ingress.kubernetes.io/router.tls"] = "true"
			updated = true
		}
	} else if _, exists := ing.Annotations["traefik.ingress.kubernetes.io/router.tls"]; exists {
		delete(ing.Annotations, "traefik.ingress.kubernetes.io/router.tls")
		updated = true
	}
	return updated
}

func (r *ChatUIInstanceReconciler) applyExtraIngressAnnotations(ing *networkingv1.Ingress, override bool) bool {
	if len(r.ExtraIngressAnnotations) == 0 {
		return false
	}
	if ing.Annotations == nil {
		ing.Annotations = map[string]string{}
	}
	updated := false
	for k, v := range r.ExtraIngressAnnotations {
		current, exists := ing.Annotations[k]
		if !exists || override {
			ing.Annotations[k] = v
			updated = true
			continue
		}
		if current != v {
			ing.Annotations[k] = v
			updated = true
		}
	}
	return updated
}

func (r *ChatUIInstanceReconciler) ensureIngressDNSAnnotation(ing *networkingv1.Ingress, host string) bool {
	if host == "" {
		return false
	}
	if ing.Annotations == nil {
		ing.Annotations = map[string]string{}
	}
	desired := host
	if ing.Annotations["dns.gardener.cloud/dnsnames"] == desired {
		return false
	}
	ing.Annotations["dns.gardener.cloud/dnsnames"] = desired
	return true
}

func (r *ChatUIInstanceReconciler) ensureIngressClass(ing *networkingv1.Ingress) bool {
	className := "traefik"
	if ing.Spec.IngressClassName == nil || *ing.Spec.IngressClassName != className {
		ing.Spec.IngressClassName = &className
		return true
	}
	return false
}

func (r *ChatUIInstanceReconciler) ensureIngressHTTPRule(ing *networkingv1.Ingress, host, pathPrefix, svcName string) bool {
	updated := false
	pathType := networkingv1.PathTypePrefix
	if len(ing.Spec.Rules) == 0 {
		ing.Spec.Rules = []networkingv1.IngressRule{{}}
		updated = true
	}
	rule := &ing.Spec.Rules[0]
	if rule.Host != host {
		rule.Host = host
		updated = true
	}
	if rule.HTTP == nil {
		rule.HTTP = &networkingv1.HTTPIngressRuleValue{}
		updated = true
	}
	if len(rule.HTTP.Paths) == 0 {
		rule.HTTP.Paths = []networkingv1.HTTPIngressPath{{}}
		updated = true
	}
	path := &rule.HTTP.Paths[0]
	if path.PathType == nil {
		path.PathType = &pathType
		updated = true
	}
	if *path.PathType != pathType {
		path.PathType = &pathType
		updated = true
	}
	if path.Path != pathPrefix {
		path.Path = pathPrefix
		updated = true
	}
	if path.Backend.Service == nil {
		path.Backend.Service = &networkingv1.IngressServiceBackend{}
		updated = true
	}
	if path.Backend.Service.Name != svcName {
		path.Backend.Service.Name = svcName
		updated = true
	}
	if path.Backend.Service.Port.Number != 8080 {
		path.Backend.Service.Port = networkingv1.ServiceBackendPort{Number: 8080}
		updated = true
	}
	return updated
}

func (r *ChatUIInstanceReconciler) ensureIngressTLS(ing *networkingv1.Ingress, host string, isHTTPS bool) bool {
	desiredTLS := strings.TrimSpace(r.TLSSecretName)
	if !isHTTPS || desiredTLS == "" {
		return false
	}
	if len(ing.Spec.TLS) == 0 || ing.Spec.TLS[0].SecretName != desiredTLS || len(ing.Spec.TLS[0].Hosts) == 0 || ing.Spec.TLS[0].Hosts[0] != host {
		ing.Spec.TLS = []networkingv1.IngressTLS{{
			Hosts:      []string{host},
			SecretName: desiredTLS,
		}}
		return true
	}
	return false
}

func (r *ChatUIInstanceReconciler) evaluateInstanceReadiness(ctx context.Context, inst *uiapiv1alpha1.ChatUIInstance, svcName, instanceHost string) (bool, string, string, error) {
	deployName := fmt.Sprintf("%s-chatui", inst.Name)
	var deploy appsv1.Deployment
	if err := r.Get(ctx, client.ObjectKey{Namespace: inst.Namespace, Name: deployName}, &deploy); err != nil {
		if apierrors.IsNotFound(err) {
			return false, "DeploymentNotFound", fmt.Sprintf("Deployment %q not found", deployName), nil
		}
		return false, "", "", err
	}

	desiredReplicas := int32(1)
	if deploy.Spec.Replicas != nil && *deploy.Spec.Replicas > 0 {
		desiredReplicas = *deploy.Spec.Replicas
	}

	if deploy.Status.ObservedGeneration < deploy.Generation {
		return false, "DeploymentProgressing", fmt.Sprintf("Deployment %q has not observed latest generation", deployName), nil
	}
	if deploy.Status.UpdatedReplicas < desiredReplicas {
		return false, "DeploymentProgressing", fmt.Sprintf("Deployment %q updated replicas %d/%d", deployName, deploy.Status.UpdatedReplicas, desiredReplicas), nil
	}
	if deploy.Status.ReadyReplicas < desiredReplicas {
		return false, "DeploymentNotReady", fmt.Sprintf("Deployment %q ready replicas %d/%d", deployName, deploy.Status.ReadyReplicas, desiredReplicas), nil
	}
	if deploy.Status.AvailableReplicas < desiredReplicas {
		return false, "DeploymentNotAvailable", fmt.Sprintf("Deployment %q available replicas %d/%d", deployName, deploy.Status.AvailableReplicas, desiredReplicas), nil
	}

	endpointsReady, message, err := r.serviceHasReadyEndpoints(ctx, inst.Namespace, svcName, 8080)
	if err != nil {
		return false, "", "", err
	}
	if !endpointsReady {
		return false, "ServiceEndpointsMissing", message, nil
	}

	probeURL := r.serviceProbeURL(inst.Namespace, svcName, 8080, "/")
	if err := r.runServiceHealthCheck(ctx, probeURL); err != nil {
		return false, "HealthCheckFailed", fmt.Sprintf("Service probe failed for %s: %v", probeURL, err), nil
	}

	if err := r.runDNSCheck(ctx, instanceHost); err != nil {
		return false, "DNSNotReady", fmt.Sprintf("DNS not resolving for %s: %v", instanceHost, err), nil
	}
	return true, "Provisioned", "Chat UI is ready", nil
}

func (r *ChatUIInstanceReconciler) serviceHasReadyEndpoints(ctx context.Context, namespace, serviceName string, expectedPort int32) (bool, string, error) {
	var endpoints corev1.Endpoints
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: serviceName}, &endpoints); err != nil {
		if apierrors.IsNotFound(err) {
			return false, fmt.Sprintf("Endpoints for Service %q not found", serviceName), nil
		}
		return false, "", err
	}
	for _, subset := range endpoints.Subsets {
		if len(subset.Addresses) == 0 {
			continue
		}
		for _, port := range subset.Ports {
			if port.Port == expectedPort {
				return true, "", nil
			}
		}
	}
	return false, fmt.Sprintf("Service %q has no ready endpoints on port %d", serviceName, expectedPort), nil
}

func (r *ChatUIInstanceReconciler) serviceProbeURL(namespace, serviceName string, port int32, path string) string {
	return fmt.Sprintf("http://%s.%s.svc.cluster.local:%d%s", serviceName, namespace, port, path)
}

func (r *ChatUIInstanceReconciler) runServiceHealthCheck(ctx context.Context, targetURL string) error {
	checker := r.ServiceHealthChecker
	if checker == nil {
		checker = defaultServiceHealthChecker
	}
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return checker(probeCtx, targetURL)
}

func defaultServiceHealthChecker(ctx context.Context, targetURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}
	return nil
}

func (r *ChatUIInstanceReconciler) runDNSCheck(ctx context.Context, host string) error {
	checker := r.DNSChecker
	if checker == nil {
		checker = defaultDNSChecker
	}
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return checker(checkCtx, host)
}

func defaultDNSChecker(ctx context.Context, host string) error {
	ips, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil {
		return err
	}
	if len(ips) == 0 {
		return fmt.Errorf("no addresses found")
	}
	return nil
}

func (r *ChatUIInstanceReconciler) updateProvisioningStatus(ctx context.Context, inst *uiapiv1alpha1.ChatUIInstance, host, reason, message string) error {
	cond := metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		ObservedGeneration: inst.Generation,
		Reason:             reason,
		Message:            message,
	}
	meta.SetStatusCondition(&inst.Status.Conditions, cond)
	inst.Status.Phase = "Provisioning"
	inst.Status.URL = r.instanceURL(host)
	inst.Status.ObservedGeneration = inst.Generation
	return r.Status().Update(ctx, inst)
}

func (r *ChatUIInstanceReconciler) updateReadyStatus(ctx context.Context, inst *uiapiv1alpha1.ChatUIInstance, host string) error {
	readyCond := metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		ObservedGeneration: inst.Generation,
		Reason:             "Provisioned",
		Message:            "Chat UI is ready",
	}
	meta.SetStatusCondition(&inst.Status.Conditions, readyCond)
	inst.Status.Phase = "Ready"
	inst.Status.URL = r.instanceURL(host)
	inst.Status.ObservedGeneration = inst.Generation
	return r.Status().Update(ctx, inst)
}

func (r *ChatUIInstanceReconciler) instanceURL(host string) string {
	scheme := r.PublicScheme
	if scheme == "" {
		scheme = "http"
	}
	if strings.TrimSpace(host) == "" {
		return ""
	}
	return fmt.Sprintf("%s://%s", scheme, host)
}

func (r *ChatUIInstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&uiapiv1alpha1.ChatUIInstance{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&networkingv1.Ingress{}).
		Watches(
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.findChatUIInstancesForSecret),
		).
		Complete(r)
}

// findChatUIInstancesForSecret returns reconcile requests for all ChatUIInstances
// that reference the given Secret via credentialsSecretRef.
func (r *ChatUIInstanceReconciler) findChatUIInstancesForSecret(ctx context.Context, obj client.Object) []reconcile.Request {
	secret, ok := obj.(*corev1.Secret)
	if !ok {
		return nil
	}

	var instances uiapiv1alpha1.ChatUIInstanceList
	if err := r.List(ctx, &instances, client.InNamespace(secret.Namespace)); err != nil {
		return nil
	}

	var requests []reconcile.Request
	for _, inst := range instances.Items {
		if strings.TrimSpace(inst.Spec.CredentialsSecretRef.Name) == secret.Name {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      inst.Name,
					Namespace: inst.Namespace,
				},
			})
		}
	}
	return requests
}

// computeSecretChecksum returns a SHA256 hash of the Secret's data.
// It sorts keys to ensure deterministic output.
func computeSecretChecksum(secret *corev1.Secret) string {
	if secret == nil || len(secret.Data) == 0 {
		return ""
	}

	keys := make([]string, 0, len(secret.Data))
	for k := range secret.Data {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write(secret.Data[k])
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func generateSlug(length int) (string, error) {
	if length <= 0 {
		return "", fmt.Errorf("slug length must be greater than zero")
	}
	randomBytes := make([]byte, length)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", err
	}

	alphabet := []byte(slugAlphabet)
	slug := make([]byte, length)
	for i, b := range randomBytes {
		slug[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(slug), nil
}

func isValidSlug(slug string) bool {
	if slug == "" || len(slug) > 63 {
		return false
	}
	if slug[0] == '-' || slug[len(slug)-1] == '-' {
		return false
	}
	for i := 0; i < len(slug); i++ {
		c := slug[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			continue
		}
		if c == '-' && i != 0 && i != len(slug)-1 {
			continue
		}
		return false
	}
	return true
}

func (r *ChatUIInstanceReconciler) instanceHost(slug string) (string, error) {
	base := strings.TrimSpace(r.PublicHost)
	if base == "" {
		return "", fmt.Errorf("PUBLIC_HOST is not configured")
	}
	return fmt.Sprintf("%s.%s", slug, base), nil
}

func ptrTo[T any](v T) *T {
	return &v
}

func ensureChatUIContainerProbes(c *corev1.Container) bool {
	updated := false
	readinessProbe := chatUIHTTPProbe(5, 2, 6)
	if !probesEqual(c.ReadinessProbe, readinessProbe) {
		c.ReadinessProbe = readinessProbe
		updated = true
	}
	startupProbe := chatUIHTTPProbe(10, 2, 60)
	if !probesEqual(c.StartupProbe, startupProbe) {
		c.StartupProbe = startupProbe
		updated = true
	}
	livenessProbe := chatUIHTTPProbe(10, 2, 6)
	livenessProbe.InitialDelaySeconds = 20
	if !probesEqual(c.LivenessProbe, livenessProbe) {
		c.LivenessProbe = livenessProbe
		updated = true
	}
	return updated
}

func chatUIHTTPProbe(periodSeconds, timeoutSeconds, failureThreshold int32) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/",
				Port: intstr.FromInt(8080),
			},
		},
		PeriodSeconds:    periodSeconds,
		TimeoutSeconds:   timeoutSeconds,
		FailureThreshold: failureThreshold,
	}
}

func probesEqual(actual, desired *corev1.Probe) bool {
	if actual == nil || desired == nil {
		return actual == desired
	}
	if actual.HTTPGet == nil || desired.HTTPGet == nil {
		return actual.HTTPGet == desired.HTTPGet
	}
	return actual.HTTPGet.Path == desired.HTTPGet.Path &&
		actual.HTTPGet.Port == desired.HTTPGet.Port &&
		actual.InitialDelaySeconds == desired.InitialDelaySeconds &&
		actual.PeriodSeconds == desired.PeriodSeconds &&
		actual.TimeoutSeconds == desired.TimeoutSeconds &&
		actual.FailureThreshold == desired.FailureThreshold
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
