package controller

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	uiapi "github.com/apeirora/showroom-msp-chat-ui/api/v1alpha1"
	"github.com/apeirora/showroom-msp-chat-ui/internal/accessgate"
	"github.com/apeirora/showroom-msp-chat-ui/internal/accesspolicy"
	"github.com/apeirora/showroom-msp-chat-ui/internal/webuiguard"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const accessRevisionAnnotation = "ui.privatellms.msp/access-revision"

type accessBundle struct{ name, sessionName, revision, publicURL string }

func uidHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:16])
}

func sessionSecretName(inst *uiapi.ChatUIInstance) string {
	return "chatui-session-" + uidHash(string(inst.UID))
}

func ownedByInstance(object metav1.Object, inst *uiapi.ChatUIInstance) bool {
	owner := metav1.GetControllerOf(object)
	return inst.UID != "" && object.GetNamespace() == inst.Namespace && owner != nil && owner.UID == inst.UID &&
		owner.Name == inst.Name && owner.APIVersion == uiapi.GroupVersion.String() && owner.Kind == "ChatUIInstance"
}

func (r *ChatUIInstanceReconciler) sessionSecret(ctx context.Context, inst *uiapi.ChatUIInstance) (*corev1.Secret, error) {
	var secret corev1.Secret
	key := client.ObjectKey{Namespace: inst.Namespace, Name: sessionSecretName(inst)}
	if err := r.routeReader().Get(ctx, key, &secret); err == nil {
		if !ownedByInstance(&secret, inst) || secret.Immutable == nil || !*secret.Immutable ||
			secret.UID == "" || inst.Status.AccessSecretUIDs[secret.Name] != string(secret.UID) {
			return nil, errors.New("session Secret ownership or immutability mismatch")
		}
		for _, name := range []string{"cookie-secret", "webui-secret"} {
			data, err := base64.StdEncoding.DecodeString(string(secret.Data[name]))
			if err != nil || len(data) != 32 {
				return nil, errors.New("invalid session key material")
			}
		}
		return &secret, nil
	} else if !apierrors.IsNotFound(err) {
		return nil, err
	}
	if inst.Status.AccessSecretUIDs[key.Name] != "" {
		return nil, errors.New("pinned session Secret is missing; operator recovery required")
	}
	secret = corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}, Type: corev1.SecretTypeOpaque, Immutable: ptrTo(true), Data: map[string][]byte{}}
	for _, name := range []string{"cookie-secret", "webui-secret"} {
		data := make([]byte, 32)
		if _, err := rand.Read(data); err != nil {
			return nil, errors.New("session entropy unavailable")
		}
		secret.Data[name] = []byte(base64.StdEncoding.EncodeToString(data))
	}
	if err := ctrl.SetControllerReference(inst, &secret, r.Scheme); err != nil {
		return nil, err
	}
	if err := r.Create(ctx, &secret); err != nil {
		return nil, err
	}
	if err := r.pinAccessSecret(ctx, inst, &secret); err != nil {
		return nil, err
	}
	return &secret, nil
}

// Only the result of our successful Create may establish a pin. A crash before
// persistence leaves an untrusted orphan and requires explicit operator recovery.
// Read back a fresh object: older CRDs must not silently prune this boundary.
func (r *ChatUIInstanceReconciler) pinAccessSecret(ctx context.Context, inst *uiapi.ChatUIInstance, secret *corev1.Secret) error {
	if secret.UID == "" {
		return errors.New("API server did not assign an access Secret UID")
	}
	before := inst.DeepCopy()
	pins := map[string]string{secret.Name: string(secret.UID)}
	if uid := inst.Status.AccessSecretUIDs[sessionSecretName(inst)]; uid != "" {
		pins[sessionSecretName(inst)] = uid
	}
	inst.Status.AccessSecretUIDs = pins
	if err := r.Status().Patch(ctx, inst, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
		return err
	}
	var persisted uiapi.ChatUIInstance
	if err := r.routeReader().Get(ctx, client.ObjectKeyFromObject(inst), &persisted); err != nil {
		return err
	}
	if !reflect.DeepEqual(persisted.Status.AccessSecretUIDs, pins) {
		return errors.New("ChatUIInstance schema must persist access Secret UID pins")
	}
	*inst = persisted
	return nil
}

func oauthConfiguration(e accesspolicy.Entry) ([]byte, error) {
	return json.Marshal(map[string]any{
		"server": map[string]any{"bindAddress": "0.0.0.0:8080"},
		"providers": []any{map[string]any{
			"id": "chatui", "provider": "oidc", "clientID": e.ClientID,
			"clientSecretFile": "/etc/chat-ui-access/client-secret", "scope": "openid email",
			"code_challenge_method": "S256", "skipClaimsFromProfileURL": true,
			"oidcConfig": map[string]any{"issuerURL": e.Issuer, "insecureSkipNonce": false, "userIDClaim": "sub", "emailClaim": "sub"},
		}},
		"upstreamConfig": map[string]any{"upstreams": []any{map[string]any{
			"id": "instance-access", "path": "/", "uri": "http://127.0.0.1:8082/", "passHostHeader": true, "proxyWebSockets": true, "timeout": "0s",
		}}},
		"injectRequestHeaders": []any{map[string]any{"name": accessgate.TokenHeader, "preserveRequestValue": false,
			"values": []any{map[string]any{"claimSource": map[string]any{"claim": "id_token"}}}}},
		"injectResponseHeaders": []any{},
	})
}

func oauthArguments(publicURL string) []string {
	return []string{"--alpha-config=/etc/chat-ui-access/oauth.json",
		"--redirect-url=" + publicURL + "/oauth2/callback", "--cookie-name=" + accessgate.CookieName,
		"--cookie-secret-file=/etc/chat-ui-session/cookie-secret", "--cookie-secure=true", "--cookie-httponly=true",
		"--cookie-samesite=lax", "--cookie-expire=1h", "--cookie-refresh=1m", "--cookie-csrf-per-request=true",
		"--cookie-csrf-per-request-limit=5", "--email-domain=*", "--skip-provider-button=true",
		"--skip-jwt-bearer-tokens=false", "--reverse-proxy=false", "--request-logging=false", "--auth-logging=false"}
}

func (r *ChatUIInstanceReconciler) prepareAccess(ctx context.Context, inst *uiapi.ChatUIInstance, host string) (*accessBundle, error) {
	e, err := r.AccessPolicy.Select(inst)
	if err != nil {
		return nil, err
	}
	if r.PublicScheme != "https" || r.AccessPolicy.CredentialsNamespace == inst.Namespace {
		return nil, errors.New("HTTPS and isolated operator credential namespace are required")
	}
	if strings.HasPrefix(inst.Spec.CredentialsSecretRef.Name, "chatui-session-") || strings.HasPrefix(inst.Spec.CredentialsSecretRef.Name, "chatui-access-") {
		return nil, errors.New("backend credentials reference a reserved access Secret name")
	}
	var credential corev1.Secret
	if err := r.routeReader().Get(ctx, client.ObjectKey{Namespace: r.AccessPolicy.CredentialsNamespace, Name: e.ClientSecretName}, &credential); err != nil {
		return nil, errors.New("OIDC client credential unavailable")
	}
	if len(credential.Data["client_secret"]) < 16 {
		return nil, errors.New("OIDC client credential is missing or too short")
	}
	session, err := r.sessionSecret(ctx, inst)
	if err != nil {
		return nil, err
	}
	publicURL := "https://" + host
	gate, err := json.Marshal(accessgate.Config{KCPServer: r.AccessPolicy.KCPServer, KCPCA: r.AccessPolicy.KCPCA, PublicURL: publicURL,
		Issuer: e.Issuer, ClientID: e.ClientID, Source: e.Source})
	if err != nil {
		return nil, err
	}
	oauth, err := oauthConfiguration(e)
	if err != nil {
		return nil, err
	}
	data := map[string][]byte{"gate.json": gate, "oauth.json": oauth, "client-secret": credential.Data["client_secret"], "chatui_guard.py": webuiguard.Source}
	serialized, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	revision := uidHash(string(inst.UID) + string(serialized) + r.AccessPolicy.ProxyImage)
	name := "chatui-access-" + revision
	var existing corev1.Secret
	key := client.ObjectKey{Namespace: inst.Namespace, Name: name}
	if err := r.routeReader().Get(ctx, key, &existing); err == nil {
		if !ownedByInstance(&existing, inst) || existing.Immutable == nil || !*existing.Immutable || !reflect.DeepEqual(existing.Data, data) ||
			existing.UID == "" || inst.Status.AccessSecretUIDs[existing.Name] != string(existing.UID) {
			return nil, errors.New("access Secret ownership or content mismatch")
		}
	} else if !apierrors.IsNotFound(err) {
		return nil, err
	} else {
		if inst.Status.AccessSecretUIDs[name] != "" {
			return nil, errors.New("pinned access Secret is missing; operator recovery required")
		}
		secret := corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: inst.Namespace}, Type: corev1.SecretTypeOpaque, Immutable: ptrTo(true), Data: data}
		if err := ctrl.SetControllerReference(inst, &secret, r.Scheme); err != nil {
			return nil, err
		}
		if err := r.Create(ctx, &secret); err != nil {
			return nil, err
		}
		if err := r.pinAccessSecret(ctx, inst, &secret); err != nil {
			return nil, err
		}
	}
	return &accessBundle{name: name, sessionName: session.Name, revision: revision, publicURL: publicURL}, nil
}

// withdrawAccess never removes another controller's object, including an Ingress
// with the expected name but a different owner. UID preconditions prevent ABA deletes.
func (r *ChatUIInstanceReconciler) withdrawAccess(ctx context.Context, inst *uiapi.ChatUIInstance) error {
	var ing networkingv1.Ingress
	key := client.ObjectKey{Namespace: inst.Namespace, Name: inst.Name + "-chatui"}
	if err := r.routeReader().Get(ctx, key, &ing); err == nil {
		if !ownsIngress(inst, &ing) {
			return errIngressNotOwned
		}
		if err := r.Delete(ctx, &ing, client.Preconditions{UID: &ing.UID, ResourceVersion: &ing.ResourceVersion}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func (r *ChatUIInstanceReconciler) denyAccess(ctx context.Context, inst *uiapi.ChatUIInstance, cause error) (ctrl.Result, error) {
	withdrawErr := r.withdrawAccess(ctx, inst)
	_, stopErr := r.stopUnapprovedDeployment(ctx, inst, true)
	statusErr := r.updateImagePolicyStatus(ctx, inst, phaseError, "AccessNotConfigured", "Instance access configuration is unavailable; operator review is required")
	// The detailed cause is operator-only; never copy credentials or internal errors into status.
	return ctrl.Result{RequeueAfter: hostnameRecheckAfter}, errors.Join(cause, withdrawErr, stopErr, statusErr)
}

func accessSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{AllowPrivilegeEscalation: ptrTo(false), RunAsNonRoot: ptrTo(true), RunAsUser: ptrTo(int64(65532)),
		ReadOnlyRootFilesystem: ptrTo(true), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}
}

func applyAccessDeployment(deploy *appsv1.Deployment, inst *uiapi.ChatUIInstance, b *accessBundle, proxyImage string) {
	deploy.Spec.Strategy = appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}
	pod := &deploy.Spec.Template
	pod.Annotations[accessRevisionAnnotation] = b.revision
	pod.Labels["ui.privatellms.msp/instance-uid"] = string(inst.UID)
	pod.Spec.AutomountServiceAccountToken = ptrTo(false)
	pod.Spec.SecurityContext = &corev1.PodSecurityContext{FSGroup: ptrTo(int64(1000)), SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}
	app := &pod.Spec.Containers[0]
	app.Ports = nil // Only OAuth2 Proxy listens on the pod interface.
	app.SecurityContext = &corev1.SecurityContext{RunAsNonRoot: ptrTo(true), RunAsUser: ptrTo(int64(1000)), RunAsGroup: ptrTo(int64(1000)),
		AllowPrivilegeEscalation: ptrTo(false), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}
	app.Command = []string{"python", "-m", "uvicorn", "chatui_guard:app", "--app-dir=/etc/chat-ui-guard", "--host=127.0.0.1", "--port=8081", "--workers=1", "--forwarded-allow-ips=127.0.0.1"}
	app.Args = nil
	app.VolumeMounts = []corev1.VolumeMount{{Name: "webui-data", MountPath: "/app/backend/data"}, {Name: "webui-tmp", MountPath: "/tmp"}, {Name: "webui-guard", MountPath: "/etc/chat-ui-guard", ReadOnly: true}}
	app.Env = append(app.Env, corev1.EnvVar{Name: "HOST", Value: "127.0.0.1"}, corev1.EnvVar{Name: "PORT", Value: "8081"},
		corev1.EnvVar{Name: "HOME", Value: "/tmp"}, corev1.EnvVar{Name: "HF_HOME", Value: "/tmp/huggingface"},
		corev1.EnvVar{Name: "WEBUI_AUTH_TRUSTED_EMAIL_HEADER", Value: accessgate.EmailHeader}, corev1.EnvVar{Name: "WEBUI_AUTH_TRUSTED_ROLE_HEADER", Value: accessgate.RoleHeader},
		corev1.EnvVar{Name: "CORS_ALLOW_ORIGIN", Value: b.publicURL},
		corev1.EnvVar{Name: "DEFAULT_USER_ROLE", Value: "user"}, corev1.EnvVar{Name: "WEBUI_AUTH_COOKIE_SECURE", Value: "true"},
		corev1.EnvVar{Name: "ENABLE_INITIAL_ADMIN_SIGNUP", Value: "false"}, corev1.EnvVar{Name: "JWT_EXPIRES_IN", Value: "1h"},
		corev1.EnvVar{Name: "WEBUI_AUTH_SIGNOUT_REDIRECT_URL", Value: "/oauth2/sign_out"})
	probe := &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"python", "-c", "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8081/health', timeout=2)"}}}, PeriodSeconds: 10, TimeoutSeconds: 3, FailureThreshold: 6}
	app.ReadinessProbe = probe.DeepCopy()
	app.LivenessProbe = probe.DeepCopy()
	app.StartupProbe = probe.DeepCopy()
	app.StartupProbe.FailureThreshold = 60
	gate := corev1.Container{Name: "instance-access", Image: proxyImage, Command: []string{"/access-proxy"}, Args: []string{"--config=/etc/chat-ui-access/gate.json"},
		SecurityContext: accessSecurityContext(), Ports: []corev1.ContainerPort{{Name: "access-health", ContainerPort: 8083}},
		VolumeMounts:   []corev1.VolumeMount{{Name: "gate-config", MountPath: "/etc/chat-ui-access", ReadOnly: true}},
		ReadinessProbe: chatUIHTTPProbe(5, 6), LivenessProbe: chatUIHTTPProbe(10, 6)}
	gate.ReadinessProbe.HTTPGet.Path = "/readyz"
	gate.ReadinessProbe.HTTPGet.Port = intstr.FromInt(8083)
	gate.LivenessProbe.HTTPGet.Path = "/readyz"
	gate.LivenessProbe.HTTPGet.Port = intstr.FromInt(8083)
	oauth := corev1.Container{Name: "oauth2-proxy", Image: accesspolicy.OAuthImage, Args: oauthArguments(b.publicURL), SecurityContext: accessSecurityContext(),
		Ports:          []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}},
		VolumeMounts:   []corev1.VolumeMount{{Name: "oauth-config", MountPath: "/etc/chat-ui-access", ReadOnly: true}, {Name: "oauth-session", MountPath: "/etc/chat-ui-session", ReadOnly: true}},
		ReadinessProbe: chatUIHTTPProbe(5, 6), LivenessProbe: chatUIHTTPProbe(10, 6)}
	oauth.ReadinessProbe.HTTPGet.Path = "/ping"
	oauth.LivenessProbe.HTTPGet.Path = "/ping"
	pod.Spec.Containers = append(pod.Spec.Containers, gate, oauth)
	pod.Spec.Volumes = []corev1.Volume{
		{Name: "webui-guard", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: b.name, Items: []corev1.KeyToPath{{Key: "chatui_guard.py", Path: "chatui_guard.py"}}}}},
		{Name: "gate-config", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: b.name, Items: []corev1.KeyToPath{{Key: "gate.json", Path: "gate.json"}}}}},
		{Name: "oauth-config", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: b.name, Items: []corev1.KeyToPath{{Key: "oauth.json", Path: "oauth.json"}, {Key: "client-secret", Path: "client-secret"}}}}},
		{Name: "oauth-session", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: b.sessionName, Items: []corev1.KeyToPath{{Key: "cookie-secret", Path: "cookie-secret"}}}}},
		{Name: "webui-data", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: "webui-tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	}
	// Normalize API defaults once so a normal API round trip does not trigger a cutover.
	*pod = managedAccessTemplate(*pod, *pod)
}

func managedAccessTemplate(existing, desired corev1.PodTemplateSpec) corev1.PodTemplateSpec {
	result := *existing.DeepCopy()
	if result.Annotations == nil {
		result.Annotations = map[string]string{}
	}
	if result.Labels == nil {
		result.Labels = map[string]string{}
	}
	for key, value := range desired.Annotations {
		result.Annotations[key] = value
	}
	for key, value := range desired.Labels {
		result.Labels[key] = value
	}
	result.Spec.Containers = desired.DeepCopy().Spec.Containers
	for i := range result.Spec.Containers {
		c := &result.Spec.Containers[i]
		c.ImagePullPolicy = corev1.PullIfNotPresent
		c.TerminationMessagePath, c.TerminationMessagePolicy = corev1.TerminationMessagePathDefault, corev1.TerminationMessageReadFile
		for j := range c.Ports {
			c.Ports[j].Protocol = corev1.ProtocolTCP
		}
		// Preserve resource sizing admitted by the platform, not executable fields.
		for _, old := range existing.Spec.Containers {
			if old.Name == c.Name {
				c.Resources = old.Resources
			}
		}
		for _, probe := range []*corev1.Probe{c.ReadinessProbe, c.LivenessProbe, c.StartupProbe} {
			if probe == nil {
				continue
			}
			probe.SuccessThreshold = 1
			if probe.HTTPGet != nil {
				probe.HTTPGet.Scheme = corev1.URISchemeHTTP
			}
		}
	}
	result.Spec.InitContainers = nil
	result.Spec.Volumes = desired.DeepCopy().Spec.Volumes
	for i := range result.Spec.Volumes {
		if secret := result.Spec.Volumes[i].Secret; secret != nil {
			secret.DefaultMode = ptrTo(int32(0644))
		}
	}
	result.Spec.SecurityContext = desired.Spec.SecurityContext.DeepCopy()
	result.Spec.AutomountServiceAccountToken = ptrTo(false)
	result.Spec.HostNetwork, result.Spec.HostPID, result.Spec.HostIPC = false, false, false
	result.Spec.ShareProcessNamespace = nil
	return result
}

// waitForAccessCutover stops the old workload and confirms no matching pods remain.
// A Deployment's replicas=0 alone does not prove that terminating pods are gone.
func (r *ChatUIInstanceReconciler) waitForAccessCutover(ctx context.Context, inst *uiapi.ChatUIInstance, existing *appsv1.Deployment) (bool, error) {
	if err := r.withdrawAccess(ctx, inst); err != nil {
		return false, err
	}
	if existing.Spec.Replicas == nil || *existing.Spec.Replicas != 0 {
		existing.Spec.Replicas = ptrTo(int32(0))
		return true, r.Update(ctx, existing)
	}
	var pods corev1.PodList
	if existing.Spec.Selector == nil || len(existing.Spec.Selector.MatchLabels) == 0 {
		return false, errors.New("cannot establish prior Deployment pod ownership")
	}
	selector, err := metav1.LabelSelectorAsSelector(existing.Spec.Selector)
	if err != nil {
		return false, err
	}
	if err := r.routeReader().List(ctx, &pods, client.InNamespace(inst.Namespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return false, err
	}
	if existing.Status.ObservedGeneration < existing.Generation || existing.Status.Replicas != 0 || len(pods.Items) != 0 {
		return true, nil
	}
	return false, nil
}

var errAccessCutover = fmt.Errorf("waiting for prior Chat UI pods to stop before access migration")
