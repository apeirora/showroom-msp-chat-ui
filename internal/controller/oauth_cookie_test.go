package controller

import (
	"bytes"
	"context"
	"encoding/base64"
	"reflect"
	"strings"
	"testing"

	uiapi "github.com/apeirora/showroom-msp-chat-ui/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestOAuthCookieSecretCompatibility(t *testing.T) {
	for value := 0; value < 256; value++ {
		key := bytes.Repeat([]byte{byte(value)}, 32)
		stored := []byte(base64.StdEncoding.EncodeToString(key))
		normalized, err := oauthCookieSecret(stored)
		if err != nil {
			t.Fatal(err)
		}
		// Match the pinned OAuth decoder's alphabet and padding handling.
		decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(string(normalized), "="))
		if err != nil || !bytes.Equal(decoded, key) {
			t.Fatalf("OAuth cannot recover original key for byte %d", value)
		}
		if !strings.ContainsAny(string(stored), "+/") && !bytes.Equal(stored, normalized) {
			t.Fatal("changed an already-working cookie signing string")
		}
	}
	for _, value := range []string{"", "not base64", base64.StdEncoding.EncodeToString(make([]byte, 16))} {
		if _, err := oauthCookieSecret([]byte(value)); err == nil {
			t.Fatal("accepted invalid controller session key")
		}
	}
}

func TestLegacySessionCookieIsNormalizedWithoutReplacingSession(t *testing.T) {
	ctx := context.Background()
	inst := hostnameInstance("ui", "tenant", "instance-uid")
	session := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name: sessionSecretName(inst), Namespace: inst.Namespace, UID: "legacy-session-uid",
		OwnerReferences: []metav1.OwnerReference{{APIVersion: uiapi.GroupVersion.String(), Kind: "ChatUIInstance",
			Name: inst.Name, UID: inst.UID, Controller: ptrTo(true)}},
	}, Type: corev1.SecretTypeOpaque, Immutable: ptrTo(true), Data: map[string][]byte{
		"cookie-secret": []byte(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{255}, 32))),
		"webui-secret":  []byte(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{251}, 32))),
	}}
	inst.Status.AccessSecretUIDs = map[string]string{session.Name: string(session.UID)}
	r := hostnameReconciler(t, inst, session)
	before := session.DeepCopy()
	bundle, err := r.prepareAccess(ctx, inst, "ui.example.test")
	if err != nil {
		t.Fatal(err)
	}
	var access corev1.Secret
	if err := r.Get(ctx, client.ObjectKey{Namespace: inst.Namespace, Name: bundle.name}, &access); err != nil {
		t.Fatal(err)
	}
	if access.Immutable == nil || !*access.Immutable || !ownedByInstance(&access, inst) ||
		inst.Status.AccessSecretUIDs[access.Name] != string(access.UID) {
		t.Fatal("normalized key is not in the pinned immutable access Secret")
	}
	decoded, err := base64.URLEncoding.DecodeString(string(access.Data["cookie-secret"]))
	if err != nil || !bytes.Equal(decoded, bytes.Repeat([]byte{255}, 32)) {
		t.Fatal("normalized cookie changed the underlying key")
	}
	assertOAuthCookieMount(t, inst, bundle, r.AccessPolicy.ProxyImage)
	second, err := r.prepareAccess(ctx, inst, "ui.example.test")
	if err != nil || second.name != bundle.name || second.sessionName != bundle.sessionName {
		t.Fatal("reconcile did not reuse the same normalized Secret and session")
	}
	var after corev1.Secret
	if err := r.Get(ctx, client.ObjectKeyFromObject(session), &after); err != nil {
		t.Fatal(err)
	}
	if after.UID != before.UID || !reflect.DeepEqual(after.Data, before.Data) || !*after.Immutable {
		t.Fatal("normalizing OAuth replaced or changed the immutable session Secret")
	}
}

func assertOAuthCookieMount(t *testing.T, inst *uiapi.ChatUIInstance, bundle *accessBundle, image string) {
	t.Helper()
	const oauthConfigVolume = "oauth-config"
	deployment := buildDeployment(inst, uiLabels(inst.Name), 1, "supported", "backend-checksum")
	applyAccessDeployment(&deployment, inst, bundle, image)
	for _, container := range deployment.Spec.Template.Spec.Containers {
		for _, mount := range container.VolumeMounts {
			if mount.Name == oauthConfigVolume && container.Name != "oauth2-proxy" {
				t.Fatal("OAuth credentials mounted into another container")
			}
		}
		if container.Name == "oauth2-proxy" {
			if !containsArgument(container.Args, "--cookie-secret-file=/etc/chat-ui-access/cookie-secret") ||
				len(container.VolumeMounts) != 1 || container.VolumeMounts[0].Name != oauthConfigVolume {
				t.Fatal("OAuth did not select the normalized cookie key")
			}
		}
	}
	found := false
	for _, volume := range deployment.Spec.Template.Spec.Volumes {
		if volume.Name == "oauth-session" {
			t.Fatal("OAuth still mounts the legacy session key")
		}
		if volume.Secret == nil {
			continue
		}
		for _, item := range volume.Secret.Items {
			if item.Key == "cookie-secret" {
				if volume.Name != oauthConfigVolume || volume.Secret.SecretName != bundle.name || item.Path != "cookie-secret" {
					t.Fatal("cookie key projected into an unintended container volume")
				}
				found = true
			}
		}
	}
	if !found {
		t.Fatal("normalized cookie key not projected")
	}
}

func containsArgument(args []string, wanted string) bool {
	for _, arg := range args {
		if arg == wanted {
			return true
		}
	}
	return false
}
