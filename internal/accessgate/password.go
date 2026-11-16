package accessgate

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/apeirora/showroom-msp-chat-ui/internal/accesspolicy"
)

const (
	ModePassword = "password"
	LoginPath    = "/_chatui/login"
	LogoutPath   = "/_chatui/logout"
	PingPath     = "/ping"
	// PasswordListenAddress replaces the OIDC proxy as the pod's only listener.
	PasswordListenAddress = "0.0.0.0:8080"

	PasswordSessionLifetime = 8 * time.Hour
	loginFailureLimit       = 10
	loginFailureWindow      = time.Minute
	maxLoginBody            = 4 << 10
	sessionContext          = "chatui-password-session\x00"
)

var passwordIdentity = regexp.MustCompile(`^[a-f0-9]{64}@chatui\.invalid$`)

func (c Config) validatePassword() error {
	if err := accesspolicy.HTTPSURL(c.PublicURL, true); err != nil {
		return err
	}
	if c.KCPServer != "" || c.KCPCA != "" || c.Issuer != "" || c.ClientID != "" || c.Source != (accesspolicy.Source{}) {
		return errors.New("password access takes no sign-in provider or source configuration")
	}
	if digest, err := hex.DecodeString(c.PasswordSHA256); err != nil || len(digest) != sha256.Size {
		return errors.New("password access requires a SHA-256 password digest")
	}
	if key, err := base64.StdEncoding.DecodeString(c.SessionKey); err != nil || len(key) != 32 {
		return errors.New("password access requires a 32-byte session key")
	}
	if !passwordIdentity.MatchString(c.Identity) {
		return errors.New("password access requires an operator-derived identity")
	}
	return nil
}

// passwordAccess holds only a digest of the generated password. The password has
// 256 bits of entropy, so a plain digest compared in constant time is sufficient.
type passwordAccess struct {
	digest   []byte
	key      []byte
	identity string
	now      func() time.Time

	mu          sync.Mutex
	windowStart time.Time
	failures    int
}

func newPasswordAccess(c Config) *passwordAccess {
	digest, _ := hex.DecodeString(c.PasswordSHA256)
	key, _ := base64.StdEncoding.DecodeString(c.SessionKey)
	return &passwordAccess{digest: digest, key: key, identity: c.Identity, now: time.Now}
}

func (p *passwordAccess) mac(expiry []byte) []byte {
	// Binding the digest invalidates every session if the password is replaced.
	m := hmac.New(sha256.New, p.key)
	m.Write([]byte(sessionContext))
	m.Write(p.digest)
	m.Write(expiry)
	return m.Sum(nil)
}

func (p *passwordAccess) newSession(expires time.Time) string {
	expiry := binary.BigEndian.AppendUint64(nil, uint64(expires.Unix()))
	return base64.RawURLEncoding.EncodeToString(expiry) + "." + base64.RawURLEncoding.EncodeToString(p.mac(expiry))
}

// session accepts exactly one unexpired cookie issued by this instance.
func (p *passwordAccess) session(r *http.Request) (time.Time, bool) {
	var values []string
	for _, cookie := range r.Cookies() {
		if cookie.Name == CookieName {
			values = append(values, cookie.Value)
		}
	}
	if len(values) != 1 {
		return time.Time{}, false
	}
	encodedExpiry, encodedMAC, found := strings.Cut(values[0], ".")
	expiry, err := base64.RawURLEncoding.DecodeString(encodedExpiry)
	if !found || err != nil || len(expiry) != 8 {
		return time.Time{}, false
	}
	mac, err := base64.RawURLEncoding.DecodeString(encodedMAC)
	if err != nil || !hmac.Equal(mac, p.mac(expiry)) {
		return time.Time{}, false
	}
	seconds := binary.BigEndian.Uint64(expiry)
	if seconds > uint64(1)<<62 {
		return time.Time{}, false
	}
	expires := time.Unix(int64(seconds), 0)
	return expires, expires.After(p.now())
}

// The limit is per instance, not per client: the password is shared and the
// client address is not trustworthy here. Existing sessions are unaffected.
// Admission, comparison and failure recording happen in one critical section,
// so concurrent attempts cannot exceed the limit between check and record.
func (p *passwordAccess) attempt(supplied []byte) (ok, limited bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	if now.Sub(p.windowStart) >= loginFailureWindow {
		p.windowStart, p.failures = now, 0
	}
	if p.failures >= loginFailureLimit {
		return false, true
	}
	if subtle.ConstantTimeCompare(supplied, p.digest) == 1 {
		return true, false
	}
	p.failures++
	return false, false
}

func (p *passwordAccess) setCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: value, Path: "/", MaxAge: maxAge,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
}

func (p *passwordAccess) serve(w http.ResponseWriter, r *http.Request, route string) {
	if route == LogoutPath {
		p.setCookie(w, "", -1)
		http.Redirect(w, r, LoginPath, http.StatusSeeOther)
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		if _, ok := p.session(r); ok {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		loginPage(w, http.StatusOK, "")
	case http.MethodPost:
		// Read the bounded form before taking the lock; never hold it during I/O.
		r.Body = http.MaxBytesReader(w, r.Body, maxLoginBody)
		if err := r.ParseForm(); err != nil {
			loginPage(w, http.StatusBadRequest, "The request could not be read.")
			return
		}
		supplied := sha256.Sum256([]byte(r.PostForm.Get("password")))
		ok, limited := p.attempt(supplied[:])
		if limited {
			w.Header().Set("Retry-After", strconv.Itoa(int(loginFailureWindow.Seconds())))
			loginPage(w, http.StatusTooManyRequests, "Too many attempts. Try again in a minute.")
			return
		}
		if !ok {
			loginPage(w, http.StatusUnauthorized, "That password is not correct.")
			return
		}
		p.setCookie(w, p.newSession(p.now().Add(PasswordSessionLifetime)), int(PasswordSessionLifetime.Seconds()))
		http.Redirect(w, r, "/", http.StatusSeeOther)
	default:
		w.Header().Set("Allow", "GET, HEAD, POST")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// challenge sends a browser navigation to the form and refuses everything else,
// so API calls and sockets fail closed instead of receiving an HTML page.
func (p *passwordAccess) challenge(w http.ResponseWriter, r *http.Request, upgrade bool) {
	if r.Method == http.MethodGet && !upgrade && strings.Contains(r.Header.Get("Accept"), "text/html") {
		http.Redirect(w, r, LoginPath, http.StatusSeeOther)
		return
	}
	http.Error(w, "Sign in required", http.StatusUnauthorized)
}

func servePing(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_, _ = w.Write([]byte("OK"))
}

// loginPage reflects no request data. The messages are fixed strings.
func loginPage(w http.ResponseWriter, status int, message string) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	if message != "" {
		message = `<p role="alert">` + message + `</p>`
	}
	_, _ = w.Write([]byte(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Chat UI sign in</title>
<style>body{font-family:system-ui,sans-serif;max-width:24rem;margin:15vh auto;padding:0 1rem}
input,button{font:inherit;width:100%;box-sizing:border-box;padding:.5rem;margin-top:.5rem}
p[role=alert]{color:#b00020}</style></head>
<body><h1>Chat UI</h1>
<p>Enter the password from this instance's login Secret in your account.</p>` + message + `
<form method="post" action="` + LoginPath + `">
<label for="password">Password</label>
<input id="password" name="password" type="password" autocomplete="current-password" required autofocus>
<button type="submit">Sign in</button>
</form></body></html>
`))
}
