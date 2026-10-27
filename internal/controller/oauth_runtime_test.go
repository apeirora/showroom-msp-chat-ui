package controller

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apeirora/showroom-msp-chat-ui/internal/accessgate"
	"github.com/apeirora/showroom-msp-chat-ui/internal/accesspolicy"
)

// Opt-in test uses the executable extracted from the pinned OAuth2 Proxy image.
// All listeners are loopback and all identities/keys are disposable fixtures.
//
//nolint:gocyclo // One sequential, isolated protocol fixture verifies the full redirect and refresh chain.
func TestPinnedOAuthRuntime(t *testing.T) {
	binary := os.Getenv("CHATUI_TEST_OAUTH_PROXY")
	if binary == "" {
		t.Skip("set CHATUI_TEST_OAUTH_PROXY to the pinned image executable")
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var grants atomic.Bool
	grants.Store(true)
	var minted atomic.Int32
	var mutex sync.Mutex
	nonce, challenge := "", ""
	var issuer *httptest.Server
	encode := func(value any) string { b, _ := json.Marshal(value); return base64.RawURLEncoding.EncodeToString(b) }
	token := func() string {
		minted.Add(1)
		payload := encode(map[string]any{"iss": issuer.URL, "sub": "alice", "aud": "chat-ui", "exp": time.Now().Add(time.Minute).Unix(), "iat": time.Now().Unix(), "nonce": nonce, "email": "alice@example.test", "email_verified": true})
		signed := encode(map[string]string{"alg": "RS256", "kid": "fixture"}) + "." + payload
		digest := sha256.Sum256([]byte(signed))
		sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
		return signed + "." + base64.RawURLEncoding.EncodeToString(sig)
	}
	issuer = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer.URL, "authorization_endpoint": issuer.URL + "/authorize", "token_endpoint": issuer.URL + "/token", "jwks_uri": issuer.URL + "/jwks", "response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"}, "id_token_signing_alg_values_supported": []string{"RS256"}, "code_challenge_methods_supported": []string{"S256"}})
		case "/jwks":
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]string{"kty": "RSA", "kid": "fixture", "use": "sig", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
		case "/authorize":
			if r.URL.Query().Get("code_challenge_method") != "S256" || r.URL.Query().Get("nonce") == "" {
				t.Error("PKCE or nonce missing")
				w.WriteHeader(400)
				return
			}
			mutex.Lock()
			nonce = r.URL.Query().Get("nonce")
			challenge = r.URL.Query().Get("code_challenge")
			mutex.Unlock()
			callback, _ := url.Parse(r.URL.Query().Get("redirect_uri"))
			q := callback.Query()
			q.Set("code", "fixture-code")
			q.Set("state", r.URL.Query().Get("state"))
			callback.RawQuery = q.Encode()
			http.Redirect(w, r, callback.String(), http.StatusFound)
		case "/token":
			_ = r.ParseForm()
			mutex.Lock()
			defer mutex.Unlock()
			if r.Form.Get("grant_type") == "authorization_code" {
				digest := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
				if r.Form.Get("code") != "fixture-code" || base64.RawURLEncoding.EncodeToString(digest[:]) != challenge {
					w.WriteHeader(400)
					return
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id_token": token(), "access_token": "fixture-access-token", "refresh_token": "fixture-refresh-token", "token_type": "Bearer", "expires_in": 60})
		default:
			http.NotFound(w, r)
		}
	}))
	defer issuer.Close()
	source := accesspolicy.Source{Cluster: "account", Namespace: "default", Name: "chat", UID: "source-uid"}
	kcp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != source.Path() || !grants.Load() {
			w.WriteHeader(403)
			return
		}
		parts := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), ".")
		if len(parts) != 3 {
			w.WriteHeader(401)
			return
		}
		signature, _ := base64.RawURLEncoding.DecodeString(parts[2])
		digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		if rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], signature) != nil {
			w.WriteHeader(401)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "ui.privatellms.msp/v1alpha1", "kind": "ChatUIInstance", "metadata": map[string]any{"name": source.Name, "namespace": source.Namespace, "uid": source.UID, "annotations": map[string]string{"kcp.io/cluster": source.Cluster}}})
	}))
	defer kcp.Close()
	// Production Gate always reaches this fixed loopback port.
	listener, err := net.Listen("tcp", "127.0.0.1:8081")
	if err != nil {
		t.Fatal("isolated application test port unavailable:", err)
	}
	application := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(r.Header) }))
	_ = application.Listener.Close()
	application.Listener = listener
	application.Start()
	defer application.Close()
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := free.Addr().String()
	_ = free.Close()
	target, _ := url.Parse("http://" + address)
	frontend := httptest.NewTLSServer(httputil.NewSingleHostReverseProxy(target))
	defer frontend.Close()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: issuer.Certificate().Raw})
	g, err := accessgate.New(accessgate.Config{KCPServer: kcp.URL, KCPCA: string(ca), PublicURL: frontend.URL, Issuer: issuer.URL, ClientID: "chat-ui", Source: source})
	if err != nil {
		t.Fatal(err)
	}
	gate := httptest.NewServer(g)
	defer gate.Close()
	config, err := oauthConfiguration(accesspolicy.Entry{Issuer: issuer.URL, ClientID: "chat-ui"})
	if err != nil {
		t.Fatal(err)
	}
	var alpha map[string]any
	if err := json.Unmarshal(config, &alpha); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	provider := alpha["providers"].([]any)[0].(map[string]any)
	provider["clientSecretFile"] = filepath.Join(directory, "client-secret")
	provider["caFiles"] = []string{filepath.Join(directory, "ca.pem")}
	alpha["upstreamConfig"].(map[string]any)["upstreams"].([]any)[0].(map[string]any)["uri"] = gate.URL + "/"
	alpha["server"] = map[string]any{"bindAddress": address}
	config, _ = json.Marshal(alpha)
	for name, data := range map[string][]byte{"oauth.json": config, "client-secret": []byte("fixture-client-secret"), "cookie-secret": []byte(base64.StdEncoding.EncodeToString(make([]byte, 32))), "ca.pem": ca} {
		if err := os.WriteFile(filepath.Join(directory, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	args := oauthArguments(frontend.URL)
	for i, arg := range args {
		args[i] = strings.ReplaceAll(strings.ReplaceAll(arg, "/etc/chat-ui-access/", directory+"/"), "/etc/chat-ui-session/", directory+"/")
		if strings.HasPrefix(arg, "--http-address=") {
			args[i] = "--http-address=" + address
		}
		if strings.HasPrefix(arg, "--cookie-refresh=") {
			args[i] = "--cookie-refresh=1s"
		}
	}
	var logs bytes.Buffer
	process := exec.Command(binary, args...)
	process.Stdout = &logs
	process.Stderr = &logs
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = process.Process.Kill(); _ = process.Wait() }()
	ready := false
	for i := 0; i < 100; i++ {
		res, err := http.Get(target.String() + "/ping")
		if err == nil {
			_ = res.Body.Close()
			if res.StatusCode == 200 {
				ready = true
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ready {
		_ = process.Process.Kill()
		_ = process.Wait()
		t.Fatal("pinned OAuth proxy failed startup: " + logs.String())
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(ca)
	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar, Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}}}
	get := func(path string) (int, http.Header) {
		req, _ := http.NewRequest(http.MethodGet, frontend.URL+path, nil)
		req.Header.Set("Accept", "text/html")
		req.Header.Set("Authorization", "Bearer native-webui-token")
		req.Header.Set(accessgate.TokenHeader, "forged")
		req.Header.Set(accessgate.EmailHeader, "victim@example.test")
		res, err := browser.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		var headers http.Header
		if res.StatusCode == 200 {
			if err := json.NewDecoder(res.Body).Decode(&headers); err != nil {
				t.Fatal(err)
			}
		} else {
			_, _ = io.Copy(io.Discard, res.Body)
		}
		return res.StatusCode, headers
	}
	code, headers := get("/")
	if code != 200 {
		t.Fatalf("OIDC authorization-code chain failed: HTTP %d", code)
	}
	if headers.Get(accessgate.EmailHeader) == "victim@example.test" || !strings.HasSuffix(headers.Get(accessgate.EmailHeader), "@chatui.invalid") || headers.Get(accessgate.RoleHeader) != "user" || headers.Get(accessgate.TokenHeader) != "" || strings.Contains(headers.Get("Cookie"), "chatui-session") || headers.Get("Authorization") != "Bearer native-webui-token" {
		t.Fatal("identity isolation or native Authorization transport failed")
	}
	time.Sleep(1500 * time.Millisecond)
	if code, _ := get("/"); code != 200 || minted.Load() < 2 {
		t.Fatal("OIDC refresh failed to provide a current ID token")
	}
	grants.Store(false)
	if code, _ := get("/"); code != 403 {
		t.Fatal("valid login bypassed revoked instance permission")
	}
	grants.Store(true)
	noRedirect := *browser
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := noRedirect.Get(frontend.URL + "/oauth2/sign_out")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	u, _ := url.Parse(frontend.URL)
	for _, cookie := range jar.Cookies(u) {
		if strings.HasPrefix(cookie.Name, accessgate.CookieName) && !strings.Contains(cookie.Name, "csrf") {
			t.Fatal("logout retained session cookie")
		}
	}
	t.Log("PASS pinned OAuth proxy: PKCE, nonce, TLS, code exchange, header replacement, native JWT preservation, ID-token refresh, exact permission revocation, logout")
}
