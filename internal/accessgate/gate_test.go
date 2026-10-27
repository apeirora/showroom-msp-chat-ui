package accessgate

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apeirora/showroom-msp-chat-ui/internal/accesspolicy"
)

func fixtureToken(subject string, expires time.Time) string {
	claims, _ := json.Marshal(map[string]any{"iss": "https://issuer.example.test/realm", "sub": subject, "aud": "chat-ui", "exp": expires.Unix()})
	return "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".synthetic-signature"
}

type fixture struct {
	gate     *Gate
	token    string
	status   atomic.Int32
	wrongUID atomic.Bool
	checks   atomic.Int32
	calls    atomic.Int32
	seen     chan http.Header
	kcp      *httptest.Server
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{token: fixtureToken("alice", time.Now().Add(time.Hour)), seen: make(chan http.Header, 10)}
	f.status.Store(200)
	source := accesspolicy.Source{Cluster: "account-a", Namespace: "default", Name: "chat", UID: "source-uid"}
	f.kcp = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.checks.Add(1)
		if r.Method != http.MethodGet || r.URL.String() != source.Path() || r.Header.Get("Impersonate-User") != "" {
			t.Error("caller influenced the KCP request")
		}
		// This represents KCP's credential verifier. Mutated/forged tokens must
		// not pass merely because their unverified claims look plausible.
		if r.Header.Get("Authorization") != "Bearer "+f.token {
			w.WriteHeader(401)
			return
		}
		if code := f.status.Load(); code != 200 {
			w.Header().Set("Location", "https://attacker.invalid/")
			w.WriteHeader(int(code))
			return
		}
		uid := source.UID
		if f.wrongUID.Load() {
			uid = "recreated-source"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "ui.privatellms.msp/v1alpha1", "kind": "ChatUIInstance",
			"metadata": map[string]any{"name": source.Name, "namespace": source.Namespace, "uid": uid, "annotations": map[string]string{"kcp.io/cluster": source.Cluster}}})
	}))
	f.kcp.Config.ErrorLog = log.New(io.Discard, "", 0)
	t.Cleanup(f.kcp.Close)
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		f.seen <- r.Header.Clone()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("application"))
	}))
	t.Cleanup(app.Close)
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.kcp.Certificate().Raw})
	var err error
	f.gate, err = newGate(Config{KCPServer: f.kcp.URL, KCPCA: string(ca), PublicURL: "https://ui.example.test", Issuer: "https://issuer.example.test/realm", ClientID: "chat-ui", Source: source}, app.URL)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) request(method string) *http.Request {
	r := httptest.NewRequest(method, "https://ui.example.test/api/test?cluster=victim", nil)
	r.Header.Set(TokenHeader, f.token)
	r.Header.Set("Origin", "https://ui.example.test")
	return r
}

func TestGateAuthorizesExactInstanceAndRewritesIdentity(t *testing.T) {
	f := newFixture(t)
	r := f.request(http.MethodPost)
	r.Header.Set(EmailHeader, "victim@example.test")
	r.Header.Set(RoleHeader, "admin")
	r.Header.Set("Authorization", "Bearer webui-session")
	r.Header.Set("X-Forwarded-Access-Token", "do-not-forward")
	r.Header.Set("Impersonate-User", "administrator")
	r.Header.Set("X-Forwarded-Host", "victim.example.test")
	r.Header.Set("Cookie", CookieName+"=private-session; "+CookieName+"_csrf=private-csrf; token=application-session")
	w := httptest.NewRecorder()
	f.gate.ServeHTTP(w, r)
	if w.Code != 200 || f.checks.Load() != 1 || f.calls.Load() != 1 {
		t.Fatalf("authorized request failed: %d", w.Code)
	}
	h := <-f.seen
	digest := sha256.Sum256([]byte("https://issuer.example.test/realm\x00alice"))
	if h.Get(EmailHeader) != hex.EncodeToString(digest[:])+"@chatui.invalid" || h.Get(RoleHeader) != "user" {
		t.Fatal("caller selected application identity or administrator role")
	}
	if h.Get(TokenHeader) != "" || h.Get("X-Forwarded-Access-Token") != "" || h.Get("Impersonate-User") != "" || strings.Contains(h.Get("Cookie"), "private-") {
		t.Fatal("credential/identity header leaked to application")
	}
	if h.Get("Authorization") != "Bearer webui-session" || h.Get("Cookie") != "token=application-session" {
		t.Fatal("application session transport broken")
	}
	if h.Get("X-Forwarded-Host") != "ui.example.test" || h.Get("X-Forwarded-Proto") != "https" {
		t.Fatal("caller controlled external origin")
	}
}

func TestGateDeniesKCPFailuresRedirectsRecreationAndRevocation(t *testing.T) {
	for _, code := range []int{201, 301, 302, 307, 401, 403, 404, 429, 500, 503} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			f := newFixture(t)
			f.status.Store(int32(code))
			w := httptest.NewRecorder()
			f.gate.ServeHTTP(w, f.request(http.MethodGet))
			if w.Code != 403 || f.calls.Load() != 0 || f.checks.Load() != 1 {
				t.Fatalf("KCP response %d permitted access or redirect", code)
			}
		})
	}
	f := newFixture(t)
	w := httptest.NewRecorder()
	f.gate.ServeHTTP(w, f.request(http.MethodGet))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	f.status.Store(403)
	w = httptest.NewRecorder()
	f.gate.ServeHTTP(w, f.request(http.MethodGet))
	if w.Code != 403 || f.calls.Load() != 1 || f.checks.Load() != 2 {
		t.Fatal("cached allow survived revocation")
	}
	f.status.Store(200)
	f.wrongUID.Store(true)
	w = httptest.NewRecorder()
	f.gate.ServeHTTP(w, f.request(http.MethodGet))
	if w.Code != 403 || f.calls.Load() != 1 {
		t.Fatal("old runtime inherited access to recreated source")
	}
}

func TestGateRejectsCallerForgeriesAndCrossSiteRequests(t *testing.T) {
	for _, scenario := range []string{"missing", "duplicate", "forged", "host", "origin", "no-origin", "websocket-origin"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t)
			r := f.request(http.MethodPost)
			switch scenario {
			case "missing":
				r.Header.Del(TokenHeader)
			case "duplicate":
				r.Header.Add(TokenHeader, f.token)
			case "forged":
				r.Header.Set(TokenHeader, fixtureToken("admin", time.Now().Add(time.Hour)))
			case "host":
				r.Host = "victim.example.test"
			case "origin":
				r.Header.Set("Origin", "https://attacker.invalid")
			case "no-origin":
				r.Header.Del("Origin")
			case "websocket-origin":
				r.Method = http.MethodGet
				r.Header.Set("Upgrade", "websocket")
				r.Header.Set("Origin", "https://attacker.invalid")
			}
			w := httptest.NewRecorder()
			f.gate.ServeHTTP(w, r)
			if w.Code < 400 || f.calls.Load() != 0 {
				t.Fatalf("accepted %s: %d", scenario, w.Code)
			}
		})
	}
}

func TestGateChecksTokenAudienceIssuerAndExpiryAfterKCP(t *testing.T) {
	for _, scenario := range []string{"expired", "audience", "issuer", "not-jwt"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t)
			switch scenario {
			case "expired":
				f.token = fixtureToken("alice", time.Now().Add(-time.Hour))
			case "audience":
				f.gate.config.ClientID = "other-client"
			case "issuer":
				f.gate.config.Issuer = "https://other-issuer.example.test"
			case "not-jwt":
				f.token = "opaque-kcp-service-account-token"
			}
			w := httptest.NewRecorder()
			f.gate.ServeHTTP(w, f.request(http.MethodGet))
			if w.Code != 403 || f.calls.Load() != 0 {
				t.Fatal("accepted incompatible identity")
			}
		})
	}
}

func TestGateRequiresTLSVerification(t *testing.T) {
	f := newFixture(t)
	config := f.gate.config
	config.KCPCA = ""
	g, err := newGate(config, f.gate.upstream.String())
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	g.ServeHTTP(w, f.request(http.MethodGet))
	if w.Code != 403 || f.calls.Load() != 0 {
		t.Fatal("accepted untrusted KCP TLS")
	}
}

func TestHealthDoesNotProxyArbitraryPaths(t *testing.T) {
	f := newFixture(t)
	for _, path := range []string{"/", "/api/v1/auths/", "/readyz/../api"} {
		w := httptest.NewRecorder()
		f.gate.Health(w, httptest.NewRequest(http.MethodGet, "http://localhost"+path, nil))
		if w.Code != 404 || f.calls.Load() != 0 {
			t.Fatal("health port allowed arbitrary application access")
		}
	}
}

func TestGateBlocksNativeCredentialsAndHeaderAliases(t *testing.T) {
	f := newFixture(t)
	for _, p := range []string{"/api/v1/auths/signup", "/api/v1/auths/signup/", "/api/v1/auths//signup", "/api/v1/auths/update/password", "/api/v1/auths/api_key", "/api/v1/auths/admin/config", "/api/v1/auths/oauth/a/token/exchange"} {
		r := f.request(http.MethodPost)
		r.URL.Path = p
		w := httptest.NewRecorder()
		f.gate.ServeHTTP(w, r)
		if w.Code != 403 || f.calls.Load() != 0 {
			t.Fatalf("native credential path allowed: %s", p)
		}
	}
	r := f.request(http.MethodGet)
	r.Header.Set("X_Chatui_Email", "victim")
	r.Header.Set("X_Chatui_Role", "admin")
	w := httptest.NewRecorder()
	f.gate.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	h := <-f.seen
	if h.Get("X_Chatui_Email") != "" || h.Get("X_Chatui_Role") != "" {
		t.Fatal("underscore identity alias survived")
	}
	for _, header := range []string{"Upgrade", "Connection"} {
		r = f.request(http.MethodGet)
		r.Header.Del("Origin")
		r.Header.Set(header, "upgrade")
		w = httptest.NewRecorder()
		f.gate.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal("upgrade without origin permitted")
		}
	}
}

func TestGateClosesUpgradedConnectionAtTokenExpiry(t *testing.T) {
	f := newFixture(t)
	f.token = fixtureToken("alice", time.Now().Add(2*time.Second))
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		_, _ = io.Copy(io.Discard, conn)
	}))
	defer upstream.Close()
	g, err := newGate(f.gate.config, upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(g)
	defer server.Close()
	conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
	_, _ = fmt.Fprintf(conn, "GET /ws/socket.io/ HTTP/1.1\r\nHost: ui.example.test\r\nOrigin: https://ui.example.test\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n%s: %s\r\n\r\n", TokenHeader, f.token)
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, nil)
	if err != nil || response.StatusCode != 101 {
		t.Fatalf("upgrade failed: %v", err)
	}
	_, err = reader.ReadByte()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("connection not closed on credential expiry: %v", err)
	}
}

func TestGateClosesActiveSocketAfterPermissionRevocation(t *testing.T) {
	f := newFixture(t)
	f.token = fixtureToken("alice", time.Now().Add(time.Hour))
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		_, _ = io.Copy(io.Discard, conn)
	}))
	defer upstream.Close()
	g, err := newGate(f.gate.config, upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	g.recheckInterval = 30 * time.Millisecond
	server := httptest.NewServer(g)
	defer server.Close()
	conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
	_, _ = fmt.Fprintf(conn, "GET /ws/socket.io/ HTTP/1.1\r\nHost: ui.example.test\r\nOrigin: https://ui.example.test\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n%s: %s\r\n\r\n", TokenHeader, f.token)
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, nil)
	if err != nil || response.StatusCode != 101 {
		t.Fatalf("upgrade failed: %v", err)
	}
	f.status.Store(403)
	_, err = reader.ReadByte()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("connection not closed on permission revocation: %v", err)
	}
}
