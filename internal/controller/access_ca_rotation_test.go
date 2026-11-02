package controller

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	uiapi "github.com/apeirora/showroom-msp-chat-ui/api/v1alpha1"
	"github.com/apeirora/showroom-msp-chat-ui/internal/accessgate"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// This starts at the policy loaded by a restarted manager. It uses a fake
// Kubernetes API for reconciliation and real loopback TLS/proxy listeners. It
// does not claim to exercise OpenBao, ESO, Flux, or a Kubernetes rollout.
//
//nolint:gocyclo // One sequential fixture verifies trust rollout and denial on both sides of the signer switch.
func TestKCPTrustRotationReplacesAccessRuntime(t *testing.T) {
	ctx := context.Background()
	inst := hostnameInstance("ui", "tenant", "rotation-uid")
	r := hostnameReconciler(t, inst, hostnameCredentials(inst.Namespace))
	if err := r.Get(ctx, client.ObjectKeyFromObject(inst), inst); err != nil {
		t.Fatal(err)
	}
	entry := r.AccessPolicy.Entries[0]
	claims, err := json.Marshal(map[string]any{"iss": entry.Issuer, "sub": "alice", "aud": entry.ClientID, "exp": time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	token := "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".synthetic-signature"
	caA, leafA := rotationCertificate(t, 1)
	caB, leafB := rotationCertificate(t, 2)
	var leaf atomic.Pointer[tls.Certificate]
	leaf.Store(&leafA)
	var permissionRequests, applicationRequests atomic.Int32
	kcp := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		permissionRequests.Add(1)
		if req.Method != http.MethodGet || req.URL.Path != entry.Source.Path() || req.Header.Get("Authorization") != "Bearer "+token {
			t.Error("unexpected permission target or bearer credential")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": uiapi.GroupVersion.String(), "kind": "ChatUIInstance", "metadata": map[string]any{
			"name": entry.Source.Name, "namespace": entry.Source.Namespace, "uid": entry.Source.UID,
			"annotations": map[string]string{"kcp.io/cluster": entry.Source.Cluster},
		}})
	}))
	kcp.Config.ErrorLog = log.New(io.Discard, "", 0)
	// Every authorization call must perform a new TLS handshake, so an already
	// authenticated keep-alive connection cannot disguise stale trust.
	kcp.Config.SetKeepAlivesEnabled(false)
	kcp.TLS = &tls.Config{MinVersion: tls.VersionTLS12, GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
		return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{*leaf.Load()}}, nil
	}}
	kcp.StartTLS()
	t.Cleanup(kcp.Close)
	r.AccessPolicy.KCPServer = kcp.URL
	application := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		applicationRequests.Add(1)
		if req.Header.Get(accessgate.TokenHeader) != "" || req.Header.Get("Authorization") != "" {
			t.Error("OIDC bearer credential reached the application")
		}
		_, _ = io.WriteString(w, "application")
	}))
	_ = application.Listener.Close()
	application.Listener, err = net.Listen("tcp", "127.0.0.1:8081") // The production Gate's fixed loopback upstream.
	if err != nil {
		t.Fatal("isolated application test port unavailable:", err)
	}
	application.Start()
	t.Cleanup(application.Close)

	readSecret := func(name, namespace string) corev1.Secret {
		t.Helper()
		var secret corev1.Secret
		if err := r.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, &secret); err != nil {
			t.Fatal(err)
		}
		return secret
	}
	deploymentKey := client.ObjectKey{Name: inst.Name + "-chatui", Namespace: inst.Namespace}
	readDeployment := func() appsv1.Deployment {
		t.Helper()
		var deployment appsv1.Deployment
		if err := r.Get(ctx, deploymentKey, &deployment); err != nil {
			t.Fatal(err)
		}
		return deployment
	}
	prepare := func(ca string) (*accessBundle, corev1.Secret) {
		t.Helper()
		r.AccessPolicy.KCPCA = ca
		bundle, err := r.prepareAccess(ctx, inst, "ui.example.test")
		if err != nil {
			t.Fatal(err)
		}
		secret := readSecret(bundle.name, inst.Namespace)
		if !ownedByInstance(&secret, inst) || secret.Immutable == nil || !*secret.Immutable || inst.Status.AccessSecretUIDs[secret.Name] != string(secret.UID) {
			t.Fatal("access configuration is not immutable, owned, and UID-pinned")
		}
		return bundle, secret
	}
	startGate := func(secret corev1.Secret) *httptest.Server {
		t.Helper()
		var config accessgate.Config
		if err := json.Unmarshal(secret.Data["gate.json"], &config); err != nil {
			t.Fatal(err)
		}
		gate, err := accessgate.New(config)
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(gate)
		t.Cleanup(server.Close)
		return server
	}
	request := func(server *httptest.Server, allowed bool) {
		t.Helper()
		beforePermission, beforeApplication := permissionRequests.Load(), applicationRequests.Load()
		req, err := http.NewRequest(http.MethodGet, server.URL+"/api/test", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "ui.example.test"
		req.Header.Set(accessgate.TokenHeader, token)
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if allowed {
			if response.StatusCode != http.StatusOK || permissionRequests.Load() != beforePermission+1 || applicationRequests.Load() != beforeApplication+1 {
				t.Fatalf("trusted KCP did not permit the request: status %d", response.StatusCode)
			}
		} else if response.StatusCode != http.StatusForbidden || permissionRequests.Load() != beforePermission || applicationRequests.Load() != beforeApplication {
			t.Fatalf("untrusted KCP received a bearer token or application access succeeded: status %d", response.StatusCode)
		}
	}

	bundleA, secretA := prepare(caA)
	if err := r.reconcileDeployment(ctx, inst, uiLabels(inst.Name), 1, uiapi.SupportedImage, "backend-revision", bundleA); err != nil {
		t.Fatal(err)
	}
	session := readSecret(bundleA.sessionName, inst.Namespace)
	oidc := readSecret(entry.ClientSecretName, r.AccessPolicy.CredentialsNamespace)
	backend := readSecret(inst.Spec.CredentialsSecretRef.Name, inst.Namespace)
	gateA := startGate(secretA)
	request(gateA, true)
	leaf.Store(&leafB)
	request(gateA, false) // Stale running proxy denies before sending the token.

	rotate := func(ca string, previous *accessBundle) (*accessBundle, corev1.Secret) {
		t.Helper()
		bundle, secret := prepare(ca)
		if bundle.revision == previous.revision || bundle.name == previous.name || bundle.sessionName != previous.sessionName {
			t.Fatal("CA update did not replace gate configuration or changed session identity")
		}
		old := readDeployment()
		ingress := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: deploymentKey.Name, Namespace: inst.Namespace}}
		if err := ctrl.SetControllerReference(inst, ingress, r.Scheme); err != nil {
			t.Fatal(err)
		}
		if err := r.Create(ctx, ingress); err != nil {
			t.Fatal(err)
		}
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "prior-access-pod", Namespace: inst.Namespace, Labels: old.Spec.Selector.MatchLabels}}
		if err := r.Create(ctx, pod); err != nil {
			t.Fatal(err)
		}
		for attempt := 0; attempt < 2; attempt++ {
			if err := r.reconcileDeployment(ctx, inst, uiLabels(inst.Name), 1, uiapi.SupportedImage, "backend-revision", bundle); !errors.Is(err, errAccessCutover) {
				t.Fatalf("cutover accepted while an old pod exists: %v", err)
			}
			stopped := readDeployment()
			if *stopped.Spec.Replicas != 0 || !reflect.DeepEqual(stopped.Spec.Template, old.Spec.Template) {
				t.Fatal("old workload was not stopped before changing its configuration")
			}
			if err := r.Get(ctx, deploymentKey, &networkingv1.Ingress{}); !apierrors.IsNotFound(err) {
				t.Fatalf("public ingress remained during CA cutover: %v", err)
			}
		}
		if err := r.Delete(ctx, pod); err != nil {
			t.Fatal(err)
		}
		if err := r.reconcileDeployment(ctx, inst, uiLabels(inst.Name), 1, uiapi.SupportedImage, "backend-revision", bundle); err != nil {
			t.Fatal(err)
		}
		deployment := readDeployment()
		if *deployment.Spec.Replicas != 1 || deployment.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType || deployment.Spec.Template.Annotations[accessRevisionAnnotation] != bundle.revision {
			t.Fatal("new trust did not reach the replacement Deployment")
		}
		mounted := false
		for _, volume := range deployment.Spec.Template.Spec.Volumes {
			if volume.Name == "gate-config" && volume.Secret != nil && volume.Secret.SecretName == secret.Name {
				mounted = true
			}
		}
		if !mounted {
			t.Fatal("replacement access proxy does not mount its new configuration")
		}
		for _, key := range []string{"oauth.json", "client-secret", "chatui_guard.py"} {
			if !reflect.DeepEqual(secret.Data[key], secretA.Data[key]) {
				t.Fatalf("CA-only rotation changed %s", key)
			}
		}
		for _, before := range []corev1.Secret{session, oidc, backend} {
			after := readSecret(before.Name, before.Namespace)
			if before.UID != after.UID || before.ResourceVersion != after.ResourceVersion || !reflect.DeepEqual(before.Data, after.Data) {
				t.Fatalf("CA-only rotation changed unrelated Secret %s", before.Name)
			}
		}
		return bundle, secret
	}
	overlap, overlapSecret := rotate(caA+caB, bundleA)
	gateA.Close()
	gateOverlap := startGate(overlapSecret)
	request(gateOverlap, true)
	leaf.Store(&leafA)
	request(gateOverlap, true) // Publish overlap before changing the KCP signer.
	leaf.Store(&leafB)
	request(gateOverlap, true)
	bundleB, secretB := rotate(caB, overlap)
	gateOverlap.Close()
	gateB := startGate(secretB)
	request(gateB, true)
	leaf.Store(&leafA)
	request(gateB, false) // Removing A really revokes trust in A.
	leaf.Store(&leafB)
	request(gateB, true)
	unchangedA := readSecret(secretA.Name, secretA.Namespace)
	if !reflect.DeepEqual(secretA.Data, unchangedA.Data) || secretA.UID != unchangedA.UID {
		t.Fatal("rotation modified the prior immutable access configuration")
	}
	rollback, rollbackSecret := rotate(caA+caB, bundleB)
	gateB.Close()
	if rollback.name != overlap.name || rollbackSecret.UID != overlapSecret.UID {
		t.Fatal("rollback did not reuse the previously pinned overlap configuration")
	}
	gateRollback := startGate(rollbackSecret)
	leaf.Store(&leafA)
	request(gateRollback, true)
	leaf.Store(&leafB)
	request(gateRollback, true)
	bundleB, secretB = rotate(caB, rollback)
	gateRollback.Close()
	gateB = startGate(secretB)
	request(gateB, true)
	leaf.Store(&leafA)
	request(gateB, false)
	leaf.Store(&leafB)
	before := readDeployment()
	stable, _ := prepare(caB)
	if stable.name != bundleB.name || stable.revision != bundleB.revision {
		t.Fatal("unchanged CA generated another access revision")
	}
	if err := r.reconcileDeployment(ctx, inst, uiLabels(inst.Name), 1, uiapi.SupportedImage, "backend-revision", stable); err != nil {
		t.Fatal(err)
	}
	if after := readDeployment(); !reflect.DeepEqual(before, after) {
		t.Fatal("unchanged CA caused another workload update")
	}
}

func rotationCertificate(t *testing.T, serial int64) (string, tls.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(serial), NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(serial + 100), NotBefore: ca.NotBefore, NotAfter: ca.NotAfter,
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, &leafKey.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})), tls.Certificate{Certificate: [][]byte{leafDER, caDER}, PrivateKey: leafKey}
}
