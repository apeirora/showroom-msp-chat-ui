// Package accessgate authorizes each browser request against the exact KCP object.
// It runs only on pod loopback, behind the session-verifying OIDC proxy.
package accessgate

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/apeirora/showroom-msp-chat-ui/internal/accesspolicy"
)

const (
	TokenHeader               = "X-Chatui-OIDC-Token"
	EmailHeader               = "X-Chatui-Email"
	RoleHeader                = "X-Chatui-Role"
	CookieName                = "__Host-chatui-session"
	WebUIURL                  = "http://127.0.0.1:8081"
	PermissionRecheckInterval = 30 * time.Second
)

type Config struct {
	KCPServer string              `json:"kcpServer"`
	KCPCA     string              `json:"kcpCA,omitempty"`
	PublicURL string              `json:"publicURL"`
	Issuer    string              `json:"issuer"`
	ClientID  string              `json:"clientID"`
	Source    accesspolicy.Source `json:"source"`
}

func (c Config) Validate() error {
	for _, endpoint := range []string{c.KCPServer, c.PublicURL} {
		if err := accesspolicy.HTTPSURL(endpoint, true); err != nil {
			return err
		}
	}
	if err := accesspolicy.HTTPSURL(c.Issuer, false); err != nil {
		return err
	}
	if c.ClientID == "" {
		return errors.New("OIDC client ID is required")
	}
	return c.Source.Validate()
}

type Gate struct {
	config          Config
	kcp             *http.Client
	application     *http.Client
	proxy           *httputil.ReverseProxy
	upstream        *url.URL
	host            string
	recheckInterval time.Duration
}

func New(c Config) (*Gate, error) { return newGate(c, WebUIURL) }

func newGate(c Config, upstream string) (*Gate, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if c.KCPCA != "" {
		if !roots.AppendCertsFromPEM([]byte(c.KCPCA)) {
			return nil, errors.New("invalid KCP CA")
		}
	} else {
		var err error
		roots, err = x509.SystemCertPool()
		if err != nil {
			return nil, errors.New("system trust store unavailable")
		}
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
		DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSHandshakeTimeout: 5 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second, IdleConnTimeout: 30 * time.Second, MaxIdleConnsPerHost: 16}
	target, err := url.Parse(upstream)
	if err != nil {
		return nil, err
	}
	public, _ := url.Parse(c.PublicURL)
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	g := &Gate{config: c, host: public.Host, upstream: target, recheckInterval: PermissionRecheckInterval,
		kcp:         &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: noRedirect},
		application: &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 3 * time.Second, CheckRedirect: noRedirect}}
	g.proxy = &httputil.ReverseProxy{
		Rewrite: func(p *httputil.ProxyRequest) {
			p.SetURL(target)
			p.Out.Host = g.host
			// Rewrite runs after hop-by-hop fields are removed. Never retain caller
			// forwarding/identity headers, even ones nominated by Connection.
			stripIdentity(p.Out.Header)
			p.Out.Header.Set(EmailHeader, p.In.Context().Value(identityKey{}).(string))
			p.Out.Header.Set(RoleHeader, "user")
			p.Out.Header.Set("X-Forwarded-Proto", "https")
			p.Out.Header.Set("X-Forwarded-Host", g.host)
			cookies := p.Out.Cookies()
			p.Out.Header.Del("Cookie")
			for _, cookie := range cookies {
				if !strings.HasPrefix(cookie.Name, CookieName) {
					p.Out.AddCookie(cookie)
				}
			}
		},
		Transport:     &http.Transport{Proxy: nil, ResponseHeaderTimeout: 60 * time.Second},
		FlushInterval: -1,
		ErrorLog:      log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "Application unavailable", http.StatusBadGateway)
		},
	}
	return g, nil
}

type identityKey struct{}

func stripIdentity(h http.Header) {
	for name := range h {
		n := strings.ToLower(name)
		if strings.Contains(n, "_") || strings.HasPrefix(n, "x-chatui-") || strings.HasPrefix(n, "x-forwarded-") ||
			strings.HasPrefix(n, "x-auth-") || strings.HasPrefix(n, "x-remote-") ||
			strings.HasPrefix(n, "impersonate-") || n == "forwarded" || n == "x-api-key" {
			h.Del(name)
		}
	}
}

// authorize delegates signature/credential verification and permission evaluation
// to KCP over verified TLS. Claims are used only after that exact GET succeeds.
func (g *Gate) authorize(ctx context.Context, token string) (string, time.Time, error) {
	if token == "" || len(token) > 64<<10 || strings.ContainsAny(token, "\r\n\t ") {
		return "", time.Time{}, errors.New("missing credential")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.config.KCPServer+g.config.Source.Path(), nil)
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	res, err := g.kcp.Do(req)
	if err != nil {
		return "", time.Time{}, errors.New("permission check unavailable")
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return "", time.Time{}, errors.New("permission denied")
	}
	var object struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			Name              string            `json:"name"`
			Namespace         string            `json:"namespace"`
			UID               string            `json:"uid"`
			DeletionTimestamp *string           `json:"deletionTimestamp"`
			Annotations       map[string]string `json:"annotations"`
		} `json:"metadata"`
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 || json.Unmarshal(data, &object) != nil {
		return "", time.Time{}, errors.New("invalid permission response")
	}
	s := g.config.Source
	if object.APIVersion != "ui.privatellms.msp/v1alpha1" || object.Kind != "ChatUIInstance" || object.Metadata.UID != s.UID ||
		object.Metadata.Name != s.Name || object.Metadata.Namespace != s.Namespace || object.Metadata.Annotations["kcp.io/cluster"] != s.Cluster || object.Metadata.DeletionTimestamp != nil {
		return "", time.Time{}, errors.New("source instance identity changed")
	}
	return g.oidcIdentity(token)
}

func (g *Gate) oidcIdentity(token string) (string, time.Time, error) {
	var claims struct {
		Issuer          string          `json:"iss"`
		Subject         string          `json:"sub"`
		Audience        json.RawMessage `json:"aud"`
		Expires         int64           `json:"exp"`
		AuthorizedParty string          `json:"azp"`
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", time.Time{}, errors.New("OIDC token required")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || json.Unmarshal(payload, &claims) != nil {
		return "", time.Time{}, errors.New("invalid OIDC claims")
	}
	var audiences []string
	if json.Unmarshal(claims.Audience, &audiences) != nil {
		var audience string
		if json.Unmarshal(claims.Audience, &audience) != nil {
			return "", time.Time{}, errors.New("invalid audience")
		}
		audiences = []string{audience}
	}
	audienceOK := false
	for _, aud := range audiences {
		if aud == g.config.ClientID {
			audienceOK = true
		}
	}
	expires := time.Unix(claims.Expires, 0)
	if claims.Issuer != g.config.Issuer || claims.Subject == "" || !audienceOK || !expires.After(time.Now()) ||
		(len(audiences) > 1 && claims.AuthorizedParty != g.config.ClientID) ||
		(claims.AuthorizedParty != "" && claims.AuthorizedParty != g.config.ClientID) {
		return "", time.Time{}, errors.New("invalid OIDC identity")
	}
	// A stable pseudonymous application identity avoids trusting mutable email
	// or giving an account read permission application-administrator privileges.
	digest := sha256.Sum256([]byte(claims.Issuer + "\x00" + claims.Subject))
	return hex.EncodeToString(digest[:]) + "@chatui.invalid", expires, nil
}

func (g *Gate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Host != g.host {
		http.Error(w, "Invalid host", http.StatusBadRequest)
		return
	}
	unsafe := r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions
	upgrade := r.Header.Get("Upgrade") != ""
	for _, value := range r.Header.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			upgrade = upgrade || strings.EqualFold(strings.TrimSpace(token), "upgrade")
		}
	}
	origin := r.Header.Get("Origin")
	if len(r.Header.Values("Origin")) > 1 || (origin != "" && origin != g.config.PublicURL) || ((unsafe || upgrade) && origin == "") {
		http.Error(w, "Invalid origin", http.StatusForbidden)
		return
	}
	// Trusted-header signin is the only credential-creation path. Deny local
	// password, API-key, LDAP, administrator and OAuth-token exchange endpoints.
	p := path.Clean(r.URL.Path)
	// Only Socket.IO has message-token identity binding in the pinned guard.
	// Terminal and realtime-audio WebSockets use independent first-message JWTs.
	if upgrade && p != "/ws/socket.io" {
		http.Error(w, "WebSocket route unavailable", http.StatusForbidden)
		return
	}
	if strings.HasPrefix(p, "/api/v1/auths/") && !allowedAuthPath(p) {
		http.Error(w, "Authentication managed by the platform", http.StatusForbidden)
		return
	}
	values := r.Header.Values(TokenHeader)
	if len(values) != 1 {
		http.Error(w, "Sign in required", http.StatusUnauthorized)
		return
	}
	identity, expires, err := g.authorize(r.Context(), values[0])
	if err != nil {
		http.Error(w, "Chat UI access denied", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithDeadline(context.WithValue(r.Context(), identityKey{}, identity), expires)
	defer cancel()
	// Keep long generation streams alive while permission remains valid. A
	// running stream/socket is checked again every 30s (plus the 5s KCP timeout),
	// and never outlives the ID token that established it.
	go func() {
		ticker := time.NewTicker(g.recheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				current, _, err := g.authorize(ctx, values[0])
				if err != nil || current != identity {
					cancel()
					return
				}
			}
		}
	}()
	g.proxy.ServeHTTP(w, r.WithContext(ctx))
}

func allowedAuthPath(p string) bool {
	switch p {
	case "/api/v1/auths/signin", "/api/v1/auths/signout", "/api/v1/auths/update/profile", "/api/v1/auths/update/timezone":
		return true
	default:
		return false
	}
}

// Health serves only a fixed application probe; it cannot proxy browser traffic.
func (g *Gate) Health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || r.URL.Path != "/readyz" {
		http.NotFound(w, r)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, g.upstream.String()+"/health", nil)
	if err != nil {
		http.Error(w, "Not ready", http.StatusServiceUnavailable)
		return
	}
	res, err := g.application.Do(req)
	if err != nil {
		http.Error(w, "Not ready", http.StatusServiceUnavailable)
		return
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		http.Error(w, "Not ready", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}
