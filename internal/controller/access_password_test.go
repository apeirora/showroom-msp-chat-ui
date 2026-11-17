package controller

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"

	uiapi "github.com/apeirora/showroom-msp-chat-ui/api/v1alpha1"
	"github.com/apeirora/showroom-msp-chat-ui/internal/accessgate"
	"github.com/apeirora/showroom-msp-chat-ui/internal/accesspolicy"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// passwordReconciler models an environment where no instance is mapped and the
// platform has enabled the generated-password fallback.
func passwordReconciler(t *testing.T, objects ...client.Object) *ChatUIInstanceReconciler {
	t.Helper()
	r := hostnameReconciler(t, objects...)
	r.AccessPolicy.Entries = nil
	r.AccessPolicy.PasswordFallback = true
	return r
}

func refresh(t *testing.T, r *ChatUIInstanceReconciler, inst *uiapi.ChatUIInstance) {
	t.Helper()
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(inst), inst); err != nil {
		t.Fatal(err)
	}
}

func readSecretData(t *testing.T, r *ChatUIInstanceReconciler, namespace, name string) *corev1.Secret {
	t.Helper()
	var secret corev1.Secret
	if err := r.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, &secret); err != nil {
		t.Fatal(err)
	}
	return &secret
}

func readSecretMeta(inst *uiapi.ChatUIInstance, name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: inst.Namespace}
}

func gateConfig(t *testing.T, bundle *corev1.Secret) accessgate.Config {
	t.Helper()
	var config accessgate.Config
	if err := accesspolicy.Decode(strings.NewReader(string(bundle.Data["gate.json"])), &config); err != nil {
		t.Fatal(err)
	}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	return config
}

func TestPasswordFallbackIsOffByDefault(t *testing.T) {
	inst := hostnameInstance("ui", "tenant", "uid")
	r := hostnameReconciler(t, inst)
	r.AccessPolicy.Entries = nil
	refresh(t, r, inst)
	if _, err := r.prepareAccess(context.Background(), inst, "ui.example.test"); !errors.Is(err, accesspolicy.ErrUnmapped) {
		t.Fatalf("unmapped instance was not denied: %v", err)
	}
	var secrets corev1.SecretList
	if err := r.List(context.Background(), &secrets, client.InNamespace(inst.Namespace)); err != nil || len(secrets.Items) != 0 {
		t.Fatalf("denied instance received access material: %d %v", len(secrets.Items), err)
	}
}

func TestUnmappedInstanceGetsGeneratedPassword(t *testing.T) {
	inst := hostnameInstance("ui", "tenant", "uid")
	r := passwordReconciler(t, inst, hostnameCredentials(inst.Namespace))
	reconcileHostname(t, r, inst)
	_ = getHostnameIngress(t, r, inst)

	name := loginSecretName(inst)
	if name != "chatui-login-ui-"+uidHash("uid") {
		t.Fatalf("login Secret name does not carry the instance name and UID hash: %q", name)
	}
	login := readSecretData(t, r, inst.Namespace, name)
	password := string(login.Data[loginPasswordKey])
	if !ownedByInstance(login, inst) || login.Immutable == nil || !*login.Immutable || len(password) != loginPasswordLength ||
		len(login.Data) != 1 || login.Labels[loginSecretLabel] != "true" {
		t.Fatal("login Secret is not an owned immutable single-password object")
	}
	if inst.Status.LoginSecretName != name || inst.Status.AccessSecretUIDs[name] != string(login.UID) {
		t.Fatalf("login Secret not recorded with its pin: %q", inst.Status.LoginSecretName)
	}

	deployment := passwordDeployment(t, r, inst)
	revision := deployment.Spec.Template.Annotations[accessRevisionAnnotation]
	bundle := readSecretData(t, r, inst.Namespace, "chatui-access-"+revision)
	if len(bundle.Data) != 2 || bundle.Data["gate.json"] == nil || bundle.Data["chatui_guard.py"] == nil {
		t.Fatalf("password bundle carries OIDC material: %d keys", len(bundle.Data))
	}
	config := gateConfig(t, bundle)
	salt, _ := base64.StdEncoding.DecodeString(config.PasswordSalt)
	want, err := accessgate.PasswordVerifier(password, salt, accessgate.PasswordIterations)
	if err != nil || config.Mode != accessgate.ModePassword || config.PasswordIterations != accessgate.PasswordIterations ||
		config.PasswordVerifier != base64.StdEncoding.EncodeToString(want) ||
		config.PublicURL != inst.Status.URL || strings.Contains(string(bundle.Data["gate.json"]), password) {
		t.Fatal("gate configuration does not bind the verifier of this instance's password")
	}

	// A second reconciliation changes nothing.
	reconcileHostname(t, r, inst)
	if again := readSecretData(t, r, inst.Namespace, name); string(again.Data[loginPasswordKey]) != password || again.UID != login.UID {
		t.Fatal("password changed on reconcile")
	}
	if passwordDeployment(t, r, inst).Spec.Template.Annotations[accessRevisionAnnotation] != revision {
		t.Fatal("access revision changed on reconcile")
	}
}

func passwordDeployment(t *testing.T, r *ChatUIInstanceReconciler, inst *uiapi.ChatUIInstance) *appsv1.Deployment {
	t.Helper()
	var deployment appsv1.Deployment
	if err := r.Get(context.Background(), client.ObjectKey{Namespace: inst.Namespace, Name: inst.Name + "-chatui"}, &deployment); err != nil {
		t.Fatal(err)
	}
	return &deployment
}

func TestPasswordPodHasNoOIDCProxyAndNeverSeesThePassword(t *testing.T) {
	inst := hostnameInstance("ui", "tenant", "uid")
	r := passwordReconciler(t, inst, hostnameCredentials(inst.Namespace))
	reconcileHostname(t, r, inst)
	login := loginSecretName(inst)
	pod := passwordDeployment(t, r, inst).Spec.Template.Spec
	names := make([]string, 0, len(pod.Containers))
	for _, container := range pod.Containers {
		names = append(names, container.Name)
		for _, env := range container.Env {
			if env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil && env.ValueFrom.SecretKeyRef.Name == login {
				t.Fatal("plaintext password exposed to the workload")
			}
			if env.Name == "WEBUI_AUTH_SIGNOUT_REDIRECT_URL" && env.Value != accessgate.LogoutPath {
				t.Fatalf("sign-out points at %q", env.Value)
			}
		}
	}
	if strings.Join(names, ",") != "open-webui,instance-access" {
		t.Fatalf("unexpected containers: %v", names)
	}
	gate := pod.Containers[1]
	if len(pod.Containers[0].Ports) != 0 || len(gate.Ports) != 2 || gate.Ports[0].Name != "http" || gate.Ports[0].ContainerPort != 8080 {
		t.Fatal("the access gate must be the only listener on the Service port")
	}
	mounted := make([]string, 0, len(pod.Volumes))
	for _, volume := range pod.Volumes {
		if volume.Secret == nil {
			continue
		}
		mounted = append(mounted, volume.Name)
		if volume.Secret.SecretName == login {
			t.Fatal("plaintext password mounted into the workload")
		}
	}
	sort.Strings(mounted)
	if strings.Join(mounted, ",") != "gate-config,webui-guard" {
		t.Fatalf("unexpected Secret volumes: %v", mounted)
	}
}

func TestPasswordsAndIdentitiesAreSeparatePerInstance(t *testing.T) {
	a, b := hostnameInstance("ui", "tenant-a", "uid-a"), hostnameInstance("ui", "tenant-b", "uid-b")
	r := passwordReconciler(t, a, b)
	configs := map[string]accessgate.Config{}
	for _, inst := range []*uiapi.ChatUIInstance{a, b} {
		refresh(t, r, inst)
		bundle, err := r.prepareAccess(context.Background(), inst, inst.Namespace+".example.test")
		if err != nil || !bundle.password {
			t.Fatalf("password access not prepared: %v", err)
		}
		configs[inst.Namespace] = gateConfig(t, readSecretData(t, r, inst.Namespace, bundle.name))
	}
	x, y := configs["tenant-a"], configs["tenant-b"]
	if x.PasswordVerifier == y.PasswordVerifier || x.PasswordSalt == y.PasswordSalt || x.SessionKey == y.SessionKey || x.Identity == y.Identity {
		t.Fatal("instances share password, salt, session key or application identity")
	}
}

func TestMappedInstanceConfigurationIsUnchangedByThePasswordOption(t *testing.T) {
	inst := hostnameInstance("ui", "tenant", "uid")
	r := hostnameReconciler(t, inst)
	r.AccessPolicy.PasswordFallback = true
	refresh(t, r, inst)
	bundle, err := r.prepareAccess(context.Background(), inst, "ui.example.test")
	if err != nil || bundle.password {
		t.Fatalf("mapped instance left OIDC access: %v", err)
	}
	e := r.AccessPolicy.Entries[0]
	// The exact document released in 0.14.5: any new key would restart every mapped instance.
	want, _ := json.Marshal(struct {
		KCPServer string              `json:"kcpServer"`
		PublicURL string              `json:"publicURL"`
		Issuer    string              `json:"issuer"`
		ClientID  string              `json:"clientID"`
		Source    accesspolicy.Source `json:"source"`
	}{r.AccessPolicy.KCPServer, "https://ui.example.test", e.Issuer, e.ClientID, e.Source})
	secret := readSecretData(t, r, inst.Namespace, bundle.name)
	if string(secret.Data["gate.json"]) != string(want) || len(secret.Data) != 5 {
		t.Fatalf("mapped gate configuration changed: %s", secret.Data["gate.json"])
	}
	if inst.Status.LoginSecretName != "" || inst.Status.AccessSecretUIDs[loginSecretName(inst)] != "" {
		t.Fatal("mapped instance received a login Secret")
	}
}

func TestRemovedMappingDoesNotFallBackToPassword(t *testing.T) {
	inst := hostnameInstance("ui", "tenant", "uid")
	r := hostnameReconciler(t, inst, hostnameCredentials(inst.Namespace))
	reconcileHostname(t, r, inst)
	r.AccessPolicy.Entries = nil
	r.AccessPolicy.PasswordFallback = true
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(inst)}); !errors.Is(err, accesspolicy.ErrUnmapped) {
		t.Fatalf("instance with per-user sign-in history was not denied: %v", err)
	}
	var login corev1.Secret
	if err := r.Get(context.Background(), client.ObjectKey{Namespace: inst.Namespace, Name: loginSecretName(inst)}, &login); !apierrors.IsNotFound(err) {
		t.Fatalf("a password replaced per-user sign-in: %v", err)
	}
}

func TestMappingReplacesPasswordAccessForGood(t *testing.T) {
	inst := hostnameInstance("ui", "tenant", "uid")
	r := passwordReconciler(t, inst)
	refresh(t, r, inst)
	if bundle, err := r.prepareAccess(context.Background(), inst, "ui.example.test"); err != nil || !bundle.password {
		t.Fatalf("password access not prepared: %v", err)
	}
	configureTestAccess(t, r, inst)
	bundle, err := r.prepareAccess(context.Background(), inst, "ui.example.test")
	if err != nil || bundle.password {
		t.Fatalf("reviewed mapping did not take over: %v", err)
	}
	refresh(t, r, inst)
	if inst.Status.LoginSecretName != "" {
		t.Fatal("login Secret still published after the mapping took over")
	}
	r.AccessPolicy.Entries = nil
	if _, err := r.prepareAccess(context.Background(), inst, "ui.example.test"); !errors.Is(err, accesspolicy.ErrUnmapped) {
		t.Fatalf("old password became valid again: %v", err)
	}
}

func TestLoginSecretIsNeverAdoptedOrRegenerated(t *testing.T) {
	ctx := context.Background()
	inst := hostnameInstance("ui", "tenant", "uid")
	planted := &corev1.Secret{ObjectMeta: readSecretMeta(inst, loginSecretName(inst)), Immutable: ptrTo(true),
		Data: map[string][]byte{loginPasswordKey: []byte(strings.Repeat("a", loginPasswordLength))}}
	r := passwordReconciler(t, inst, planted)
	refresh(t, r, inst)
	if _, err := r.prepareAccess(ctx, inst, "ui.example.test"); err == nil {
		t.Fatal("adopted a login Secret the operator did not create")
	}
	if err := r.Delete(ctx, planted); err != nil {
		t.Fatal(err)
	}
	refresh(t, r, inst)
	if _, err := r.prepareAccess(ctx, inst, "ui.example.test"); err != nil {
		t.Fatal(err)
	}
	original := readSecretData(t, r, inst.Namespace, loginSecretName(inst))
	if err := r.Delete(ctx, original); err != nil {
		t.Fatal(err)
	}
	if _, err := r.prepareAccess(ctx, inst, "ui.example.test"); err == nil {
		t.Fatal("regenerated a missing pinned login Secret")
	}
	recreated := original.DeepCopy()
	recreated.UID, recreated.ResourceVersion = "", ""
	if err := r.Create(ctx, recreated); err != nil {
		t.Fatal(err)
	}
	if _, err := r.prepareAccess(ctx, inst, "ui.example.test"); err == nil {
		t.Fatal("adopted a recreated login Secret with copied ownership")
	}
}

// legacyPasswordInstance models an instance that received its password before
// the Secret name carried the instance name: Secret, pin and status as the
// operator of that release wrote them.
func legacyPasswordInstance(t *testing.T) (*ChatUIInstanceReconciler, *uiapi.ChatUIInstance, *corev1.Secret) {
	t.Helper()
	ctx := context.Background()
	inst := hostnameInstance("ui", "tenant", "uid")
	r := passwordReconciler(t, inst)
	refresh(t, r, inst)
	legacy := &corev1.Secret{ObjectMeta: readSecretMeta(inst, "chatui-login-"+uidHash("uid")), Immutable: ptrTo(true),
		Data: map[string][]byte{loginPasswordKey: []byte(strings.Repeat("b", loginPasswordLength))}}
	legacy.Labels = map[string]string{loginSecretLabel: "true"}
	if err := ctrl.SetControllerReference(inst, legacy, r.Scheme); err != nil {
		t.Fatal(err)
	}
	if err := r.Create(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	if err := r.pinSecret(ctx, inst, legacy, legacy.Name); err != nil {
		t.Fatal(err)
	}
	return r, inst, legacy
}

func TestInstanceKeepsALoginSecretNamedBeforeTheInstanceNameWasAdded(t *testing.T) {
	ctx := context.Background()
	r, inst, legacy := legacyPasswordInstance(t)
	bundle, err := r.prepareAccess(ctx, inst, "ui.example.test")
	if err != nil || !bundle.password {
		t.Fatalf("existing password instance lost access: %v", err)
	}
	config := gateConfig(t, readSecretData(t, r, inst.Namespace, bundle.name))
	salt, _ := base64.StdEncoding.DecodeString(config.PasswordSalt)
	want, err := accessgate.PasswordVerifier(string(legacy.Data[loginPasswordKey]), salt, accessgate.PasswordIterations)
	if err != nil || config.PasswordVerifier != base64.StdEncoding.EncodeToString(want) {
		t.Fatal("the existing password is no longer the one the gate accepts")
	}
	var secrets corev1.SecretList
	if err := r.List(ctx, &secrets, client.InNamespace(inst.Namespace), client.MatchingLabels{loginSecretLabel: "true"}); err != nil ||
		len(secrets.Items) != 1 || secrets.Items[0].UID != legacy.UID || inst.Status.LoginSecretName != legacy.Name {
		t.Fatalf("existing instance received a second login Secret or a new name: %d %q %v", len(secrets.Items), inst.Status.LoginSecretName, err)
	}
}

// The pin of the old-style Secret remains after a mapping clears
// status.loginSecretName; bundle retirement must still recognize it.
func TestOldLoginSecretPinSurvivesMappingAndBundleRotation(t *testing.T) {
	ctx := context.Background()
	r, inst, legacy := legacyPasswordInstance(t)
	if _, err := r.prepareAccess(ctx, inst, "ui.example.test"); err != nil {
		t.Fatal(err)
	}
	configureTestAccess(t, r, inst)
	for i := 0; i < maxAccessBundles+3; i++ {
		r.AccessPolicy.ProxyImage = "example.test/controller@sha256:" + strings.Repeat(string(rune('a'+i)), 64)
		if bundle, err := r.prepareAccess(ctx, inst, "ui.example.test"); err != nil || bundle.password {
			t.Fatalf("rotation %d failed: %v", i, err)
		}
	}
	if inst.Status.LoginSecretName != "" || inst.Status.AccessSecretUIDs[legacy.Name] != string(legacy.UID) {
		t.Fatalf("mapping did not clear the name or the old pin was lost: %q", inst.Status.LoginSecretName)
	}
}

func TestBackendCredentialsCannotClaimTheLoginSecretName(t *testing.T) {
	inst := hostnameInstance("ui", "tenant", "uid")
	inst.Spec.CredentialsSecretRef.Name = loginSecretName(inst)
	r := passwordReconciler(t, inst)
	refresh(t, r, inst)
	if _, err := r.prepareAccess(context.Background(), inst, "ui.example.test"); err == nil {
		t.Fatal("backend credentials referenced the login Secret")
	}
}

func TestLoginSecretPinSurvivesBundleRotation(t *testing.T) {
	inst := hostnameInstance("ui", "tenant", "uid")
	r := passwordReconciler(t, inst)
	refresh(t, r, inst)
	for i := 0; i < maxAccessBundles+3; i++ {
		r.AccessPolicy.ProxyImage = "example.test/controller@sha256:" + strings.Repeat(string(rune('a'+i)), 64)
		if bundle, err := r.prepareAccess(context.Background(), inst, "ui.example.test"); err != nil || !bundle.password {
			t.Fatalf("rotation %d failed: %v", i, err)
		}
	}
	name := loginSecretName(inst)
	bundles := 0
	for pinned := range inst.Status.AccessSecretUIDs {
		if strings.HasPrefix(pinned, "chatui-access-") {
			bundles++
		}
	}
	if inst.Status.AccessSecretUIDs[name] == "" || inst.Status.LoginSecretName != name || bundles != maxAccessBundles {
		t.Fatalf("login pin lost or history unbounded: %d bundles", bundles)
	}
}
