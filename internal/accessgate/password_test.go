package accessgate

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/apeirora/showroom-msp-chat-ui/internal/accesspolicy"
)

const fixturePassword = "synthetic-instance-password"

// Synthetic operator-derived identity: 64 hex characters in the reserved domain.
var fixtureIdentity = strings.Repeat("a", 64) + identityDomain

const identityDomain = "@chatui.invalid"

func passwordConfig(password string, keyByte byte) Config {
	digest := sha256.Sum256([]byte(password))
	return Config{Mode: ModePassword, PublicURL: "https://ui.example.test", PasswordSHA256: hex.EncodeToString(digest[:]),
		SessionKey: base64.StdEncoding.EncodeToString([]byte(strings.Repeat(string(keyByte), 32))), Identity: fixtureIdentity}
}

type passwordFixture struct {
	gate *Gate
	seen chan http.Header
	hold chan struct{}
}

func newPasswordFixture(t *testing.T) *passwordFixture {
	t.Helper()
	f := &passwordFixture{seen: make(chan http.Header, 10)}
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.seen <- r.Header.Clone()
		if f.hold != nil {
			select {
			case <-r.Context().Done():
			case <-f.hold:
			}
		}
		_, _ = w.Write([]byte("application"))
	}))
	t.Cleanup(app.Close)
	var err error
	if f.gate, err = newGate(passwordConfig(fixturePassword, 'k'), app.URL); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *passwordFixture) do(r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	f.gate.ServeHTTP(w, r)
	return w
}

func passwordRequest(method, target string) *http.Request {
	r := httptest.NewRequest(method, "https://ui.example.test"+target, nil)
	if method != http.MethodGet && method != http.MethodHead {
		r.Header.Set("Origin", "https://ui.example.test")
	}
	return r
}

func loginRequest(password string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "https://ui.example.test"+LoginPath, strings.NewReader(url.Values{"password": {password}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "https://ui.example.test")
	return r
}

func sessionCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == CookieName {
			return cookie
		}
	}
	return nil
}

func TestPasswordConfigurationIsExclusiveAndComplete(t *testing.T) {
	if err := passwordConfig(fixturePassword, 'k').Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Config){
		"unknown mode":     func(c *Config) { c.Mode = "open" },
		"plain HTTP":       func(c *Config) { c.PublicURL = "http://ui.example.test" },
		"provider mixed":   func(c *Config) { c.Issuer = "https://issuer.example.test/realm" },
		"KCP mixed":        func(c *Config) { c.KCPServer = "https://kcp.example.test" },
		"source mixed":     func(c *Config) { c.Source = accesspolicy.Source{Cluster: "account"} },
		"missing digest":   func(c *Config) { c.PasswordSHA256 = "" },
		"short digest":     func(c *Config) { c.PasswordSHA256 = "abcd" },
		"short key":        func(c *Config) { c.SessionKey = base64.StdEncoding.EncodeToString([]byte("short")) },
		"caller identity":  func(c *Config) { c.Identity = "alice@example.test" },
		"missing identity": func(c *Config) { c.Identity = "" },
	} {
		c := passwordConfig(fixturePassword, 'k')
		mutate(&c)
		if c.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A mapped instance must never pick up password material by accident.
	oidc := Config{KCPServer: "https://kcp.example.test", PublicURL: "https://ui.example.test", Issuer: "https://issuer.example.test/realm",
		ClientID: "chat-ui", Source: accesspolicy.Source{Cluster: "account", Namespace: "default", Name: "chat", UID: "uid"}}
	if err := oidc.Validate(); err != nil {
		t.Fatal(err)
	}
	oidc.PasswordSHA256 = passwordConfig(fixturePassword, 'k').PasswordSHA256
	if oidc.Validate() == nil {
		t.Fatal("OIDC configuration accepted password material")
	}
}

func TestPasswordLoginIssuesSessionAndFixedIdentity(t *testing.T) {
	f := newPasswordFixture(t)
	navigation := passwordRequest(http.MethodGet, "/")
	navigation.Header.Set("Accept", "text/html,application/xhtml+xml")
	if w := f.do(navigation); w.Code != http.StatusSeeOther || w.Header().Get("Location") != LoginPath {
		t.Fatalf("navigation without session: %d %q", w.Code, w.Header().Get("Location"))
	}
	if w := f.do(passwordRequest(http.MethodGet, "/api/models")); w.Code != http.StatusUnauthorized {
		t.Fatalf("API call without session: %d", w.Code)
	}
	if w := f.do(passwordRequest(http.MethodGet, LoginPath)); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `name="password"`) ||
		!strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatalf("login form: %d", w.Code)
	}
	if w := f.do(loginRequest("wrong")); w.Code != http.StatusUnauthorized || sessionCookie(t, w) != nil {
		t.Fatalf("wrong password: %d", w.Code)
	}
	w := f.do(loginRequest(fixturePassword))
	cookie := sessionCookie(t, w)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" || cookie == nil {
		t.Fatalf("correct password: %d", w.Code)
	}
	if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" || cookie.Domain != "" ||
		cookie.MaxAge != int(PasswordSessionLifetime.Seconds()) || strings.Contains(cookie.Value, fixturePassword) {
		t.Fatalf("unsafe session cookie: %#v", cookie)
	}
	if len(f.seen) != 0 {
		t.Fatal("application reached before sign in")
	}
	r := passwordRequest(http.MethodGet, "/api/models")
	r.AddCookie(cookie)
	r.AddCookie(&http.Cookie{Name: "token", Value: "application"})
	r.Header.Set(EmailHeader, "admin"+identityDomain)
	r.Header.Set(RoleHeader, "admin")
	r.Header.Set(TokenHeader, "caller-token")
	if w := f.do(r); w.Code != http.StatusOK {
		t.Fatalf("signed in request: %d", w.Code)
	}
	seen := <-f.seen
	if seen.Get(EmailHeader) != fixtureIdentity || seen.Get(RoleHeader) != "user" || seen.Get(TokenHeader) != "" ||
		strings.Contains(seen.Get("Cookie"), CookieName) || !strings.Contains(seen.Get("Cookie"), "token=application") {
		t.Fatalf("identity or cookies leaked upstream: %v", seen)
	}
	signedIn := passwordRequest(http.MethodGet, LoginPath)
	signedIn.AddCookie(cookie)
	if w := f.do(signedIn); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Fatalf("login page with session: %d", w.Code)
	}
}

func TestPasswordSessionRejectsForgeryExpiryAndOtherInstances(t *testing.T) {
	f := newPasswordFixture(t)
	valid := f.gate.password.newSession(time.Now().Add(time.Hour))
	otherKey, _ := newGate(passwordConfig(fixturePassword, 'x'), "http://127.0.0.1:1")
	otherPassword, _ := newGate(passwordConfig("another-instance-password", 'k'), "http://127.0.0.1:1")
	expiry, mac, _ := strings.Cut(valid, ".")
	later, _, _ := strings.Cut(f.gate.password.newSession(time.Now().Add(48*time.Hour)), ".")
	for name, value := range map[string]string{
		"empty":            "",
		"no signature":     expiry,
		"extended expiry":  later + "." + mac,
		"expired":          f.gate.password.newSession(time.Now().Add(-time.Second)),
		"other key":        otherKey.password.newSession(time.Now().Add(time.Hour)),
		"other password":   otherPassword.password.newSession(time.Now().Add(time.Hour)),
		"not base64":       "!!!." + mac,
		"password as text": fixturePassword,
	} {
		r := passwordRequest(http.MethodGet, "/api/models")
		r.AddCookie(&http.Cookie{Name: CookieName, Value: value})
		if w := f.do(r); w.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
	duplicate := passwordRequest(http.MethodGet, "/api/models")
	duplicate.AddCookie(&http.Cookie{Name: CookieName, Value: valid})
	duplicate.AddCookie(&http.Cookie{Name: CookieName, Value: valid})
	if w := f.do(duplicate); w.Code != http.StatusUnauthorized {
		t.Errorf("duplicate session cookies: %d", w.Code)
	}
	if len(f.seen) != 0 {
		t.Fatal("application reached with an invalid session")
	}
	accepted := passwordRequest(http.MethodGet, "/api/models")
	accepted.AddCookie(&http.Cookie{Name: CookieName, Value: valid})
	if w := f.do(accepted); w.Code != http.StatusOK {
		t.Fatalf("valid session: %d", w.Code)
	}
}

func TestPasswordLoginIsRateLimitedPerInstance(t *testing.T) {
	f := newPasswordFixture(t)
	now := time.Now()
	f.gate.password.now = func() time.Time { return now }
	for i := 0; i < loginFailureLimit; i++ {
		if w := f.do(loginRequest("wrong")); w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, w.Code)
		}
	}
	w := f.do(loginRequest(fixturePassword))
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" || sessionCookie(t, w) != nil {
		t.Fatalf("limit not enforced: %d", w.Code)
	}
	now = now.Add(loginFailureWindow)
	if w := f.do(loginRequest(fixturePassword)); w.Code != http.StatusSeeOther || sessionCookie(t, w) == nil {
		t.Fatalf("limit did not reset: %d", w.Code)
	}
}

func TestPasswordLoginLimitHoldsUnderConcurrentAttempts(t *testing.T) {
	f := newPasswordFixture(t)
	const attempts = loginFailureLimit * 5
	codes := make(chan int, attempts)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			codes <- f.do(loginRequest("wrong")).Code
		}()
	}
	close(start)
	wg.Wait()
	close(codes)
	rejected, limited := 0, 0
	for code := range codes {
		switch code {
		case http.StatusUnauthorized:
			rejected++
		case http.StatusTooManyRequests:
			limited++
		default:
			t.Fatalf("unexpected status %d", code)
		}
	}
	if rejected != loginFailureLimit || limited != attempts-loginFailureLimit {
		t.Fatalf("%d attempts were compared, limit is %d", rejected, loginFailureLimit)
	}
}

func TestPasswordLoginRejectsCrossSiteOversizedAndOtherMethods(t *testing.T) {
	f := newPasswordFixture(t)
	missingOrigin := loginRequest(fixturePassword)
	missingOrigin.Header.Del("Origin")
	crossSite := loginRequest(fixturePassword)
	crossSite.Header.Set("Origin", "https://attacker.invalid")
	wrongHost := loginRequest(fixturePassword)
	wrongHost.Host = "other.example.test"
	oversized := loginRequest(fixturePassword + strings.Repeat("a", maxLoginBody))
	put := passwordRequest(http.MethodPut, LoginPath)
	for name, c := range map[string]struct {
		request *http.Request
		code    int
	}{
		"missing origin": {missingOrigin, http.StatusForbidden},
		"cross site":     {crossSite, http.StatusForbidden},
		"wrong host":     {wrongHost, http.StatusBadRequest},
		"oversized":      {oversized, http.StatusBadRequest},
		"other method":   {put, http.StatusMethodNotAllowed},
	} {
		if w := f.do(c.request); w.Code != c.code || sessionCookie(t, w) != nil {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
}

func TestPasswordModeKeepsNativeCredentialAndSocketRestrictions(t *testing.T) {
	f := newPasswordFixture(t)
	cookie := &http.Cookie{Name: CookieName, Value: f.gate.password.newSession(time.Now().Add(time.Hour))}
	native := passwordRequest(http.MethodPost, "/api/v1/auths/add")
	native.AddCookie(cookie)
	if w := f.do(native); w.Code != http.StatusForbidden {
		t.Fatalf("native credential endpoint: %d", w.Code)
	}
	socket := passwordRequest(http.MethodGet, "/api/v1/terminals/ws")
	socket.Header.Set("Origin", "https://ui.example.test")
	socket.Header.Set("Connection", "Upgrade")
	socket.Header.Set("Upgrade", "websocket")
	socket.AddCookie(cookie)
	if w := f.do(socket); w.Code != http.StatusForbidden {
		t.Fatalf("unbound socket route: %d", w.Code)
	}
	unauthenticatedSocket := passwordRequest(http.MethodGet, "/ws/socket.io")
	unauthenticatedSocket.Header.Set("Origin", "https://ui.example.test")
	unauthenticatedSocket.Header.Set("Connection", "Upgrade")
	unauthenticatedSocket.Header.Set("Upgrade", "websocket")
	unauthenticatedSocket.Header.Set("Accept", "text/html")
	if w := f.do(unauthenticatedSocket); w.Code != http.StatusUnauthorized {
		t.Fatalf("socket without session: %d", w.Code)
	}
	if len(f.seen) != 0 {
		t.Fatal("restricted request reached the application")
	}
}

func TestPasswordPingLogoutAndSessionBoundStreams(t *testing.T) {
	f := newPasswordFixture(t)
	probe := httptest.NewRequest(http.MethodGet, "http://10.0.0.7:8080"+PingPath, nil)
	if w := f.do(probe); w.Code != http.StatusOK || w.Body.String() != "OK" {
		t.Fatalf("probe: %d", w.Code)
	}
	if w := f.do(httptest.NewRequest(http.MethodGet, "http://10.0.0.7:8080/", nil)); w.Code != http.StatusBadRequest {
		t.Fatalf("pod address reached the application route: %d", w.Code)
	}
	logout := f.do(passwordRequest(http.MethodGet, LogoutPath))
	if cleared := sessionCookie(t, logout); logout.Code != http.StatusSeeOther || logout.Header().Get("Location") != LoginPath ||
		cleared == nil || cleared.MaxAge >= 0 || cleared.Value != "" {
		t.Fatalf("logout: %d", logout.Code)
	}
	// A running response ends with the session that established it.
	f.hold = make(chan struct{})
	defer close(f.hold)
	stream := passwordRequest(http.MethodGet, "/api/chat/stream")
	stream.AddCookie(&http.Cookie{Name: CookieName, Value: f.gate.password.newSession(time.Now().Add(2 * time.Second))})
	done := make(chan int, 1)
	go func() { done <- f.do(stream).Code }()
	select {
	case code := <-done:
		if code != http.StatusBadGateway {
			t.Fatalf("stream ended with %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("stream outlived its session")
	}
}
