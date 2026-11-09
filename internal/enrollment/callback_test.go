package enrollment

import (
	"context"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var _ CallbackVerifier = (*HTTPVerifier)(nil)

// probe is an isolated TLS stub of the Keycloak authorization endpoint plus a
// callback endpoint that must never be contacted.
type probe struct {
	server       *httptest.Server
	verifier     *HTTPVerifier
	issuer       string
	callback     string
	authHits     atomic.Int32
	callbackHits atomic.Int32
	mu           sync.Mutex
	seen         []url.Values
}

func newProbe(t *testing.T, timeout time.Duration, behave func(p *probe, w http.ResponseWriter, r *http.Request, state string)) *probe {
	t.Helper()
	p := &probe{}
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth2/callback", func(http.ResponseWriter, *http.Request) { p.callbackHits.Add(1) })
	mux.HandleFunc("/realms/showroom/protocol/openid-connect/auth", func(w http.ResponseWriter, r *http.Request) {
		p.authHits.Add(1)
		q := r.URL.Query()
		want := map[string]string{"client_id": "chat-ui", "redirect_uri": p.callback, "response_type": "code",
			"scope": "openid", "prompt": "none", "code_challenge_method": "S256"}
		for key, value := range want {
			if q.Get(key) != value {
				t.Errorf("probe parameter %s = %q, want %q", key, q.Get(key), value)
			}
		}
		if len(q) != len(want)+3 || len(q.Get("code_challenge")) != 43 || len(q.Get("state")) < 32 || len(q.Get("nonce")) < 32 {
			t.Errorf("probe has missing or unexpected parameters: %v", q)
		}
		if r.Method != http.MethodGet || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Error("probe carried credentials or used a non-GET method")
		}
		p.mu.Lock()
		p.seen = append(p.seen, q)
		p.mu.Unlock()
		behave(p, w, r, q.Get("state"))
	})
	p.server = httptest.NewTLSServer(mux)
	t.Cleanup(p.server.Close)
	pool := x509.NewCertPool()
	pool.AddCert(p.server.Certificate())
	p.verifier = NewHTTPVerifier(pool, timeout)
	p.issuer = p.server.URL + "/realms/showroom"
	p.callback = p.server.URL + "/oauth2/callback"
	return p
}

func redirect(w http.ResponseWriter, status int, location string) {
	w.Header().Set("Location", location)
	w.WriteHeader(status)
}

func TestHTTPVerifierAcceptsOnlyTheExactLoginRequiredRedirect(t *testing.T) {
	type behavior func(p *probe, w http.ResponseWriter, r *http.Request, state string)
	good := func(status int, suffix string) behavior {
		return func(p *probe, w http.ResponseWriter, _ *http.Request, state string) {
			redirect(w, status, p.callback+"?error=login_required&state="+state+suffix)
		}
	}
	cases := []struct {
		name     string
		behavior behavior
		ok       bool
	}{
		{"302 login_required", good(http.StatusFound, ""), true},
		{"303 login_required", good(http.StatusSeeOther, ""), true},
		{"extra descriptive parameter", good(http.StatusFound, "&error_description=Login+required"), true},
		{"foreign host", func(p *probe, w http.ResponseWriter, _ *http.Request, state string) {
			redirect(w, 302, "https://attacker.invalid/oauth2/callback?error=login_required&state="+state)
		}, false},
		{"other path", func(p *probe, w http.ResponseWriter, _ *http.Request, state string) {
			redirect(w, 302, p.server.URL+"/other?error=login_required&state="+state)
		}, false},
		{"plain http callback", func(p *probe, w http.ResponseWriter, _ *http.Request, state string) {
			redirect(w, 302, "http"+p.callback[len("https"):]+"?error=login_required&state="+state)
		}, false},
		{"state mismatch", func(p *probe, w http.ResponseWriter, _ *http.Request, _ string) {
			redirect(w, 302, p.callback+"?error=login_required&state=other")
		}, false},
		{"state missing", func(p *probe, w http.ResponseWriter, _ *http.Request, _ string) {
			redirect(w, 302, p.callback+"?error=login_required")
		}, false},
		{"duplicate state", func(p *probe, w http.ResponseWriter, _ *http.Request, state string) {
			redirect(w, 302, p.callback+"?error=login_required&state="+state+"&state=other")
		}, false},
		{"unexpected success with code", func(p *probe, w http.ResponseWriter, _ *http.Request, state string) {
			redirect(w, 302, p.callback+"?code=abc&state="+state)
		}, false},
		{"login_required plus code", good(http.StatusFound, "&code=abc"), false},
		{"token in query", good(http.StatusFound, "&id_token=x"), false},
		{"token in fragment", func(p *probe, w http.ResponseWriter, _ *http.Request, state string) {
			redirect(w, 302, p.callback+"?error=login_required&state="+state+"#access_token=x")
		}, false},
		{"different error", func(p *probe, w http.ResponseWriter, _ *http.Request, state string) {
			redirect(w, 302, p.callback+"?error=invalid_request&state="+state)
		}, false},
		{"temporary redirect status", good(http.StatusTemporaryRedirect, ""), false},
		{"login page", func(_ *probe, w http.ResponseWriter, _ *http.Request, _ string) { w.WriteHeader(200) }, false},
		{"server error", func(_ *probe, w http.ResponseWriter, _ *http.Request, _ string) { w.WriteHeader(500) }, false},
		{"redirect without location", func(_ *probe, w http.ResponseWriter, _ *http.Request, _ string) { w.WriteHeader(302) }, false},
		{"unparseable location", func(_ *probe, w http.ResponseWriter, _ *http.Request, _ string) {
			w.Header()["Location"] = []string{"http://[::1"}
			w.WriteHeader(302)
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newProbe(t, 5*time.Second, tc.behavior)
			err := p.verifier.Verify(context.Background(), p.issuer, "chat-ui", p.callback)
			if (err == nil) != tc.ok {
				t.Fatalf("Verify error = %v, want success %v", err, tc.ok)
			}
			if p.callbackHits.Load() != 0 {
				t.Fatal("verifier followed a redirect")
			}
		})
	}
}

func TestHTTPVerifierUsesFreshParametersEachTime(t *testing.T) {
	p := newProbe(t, 5*time.Second, func(p *probe, w http.ResponseWriter, _ *http.Request, state string) {
		redirect(w, 302, p.callback+"?error=login_required&state="+state)
	})
	for i := 0; i < 2; i++ {
		if err := p.verifier.Verify(context.Background(), p.issuer, "chat-ui", p.callback); err != nil {
			t.Fatal(err)
		}
	}
	a, b := p.seen[0], p.seen[1]
	for _, key := range []string{"state", "nonce", "code_challenge"} {
		if a.Get(key) == b.Get(key) {
			t.Fatalf("%s was reused", key)
		}
	}
}

func TestHTTPVerifierRefusesUnsafeInputsAndUntrustedTLS(t *testing.T) {
	p := newProbe(t, 5*time.Second, func(p *probe, w http.ResponseWriter, _ *http.Request, state string) {
		redirect(w, 302, p.callback+"?error=login_required&state="+state)
	})
	ctx := context.Background()
	for name, call := range map[string]func() error{
		"http issuer": func() error {
			return p.verifier.Verify(ctx, "http://127.0.0.1:1/realms/showroom", "chat-ui", p.callback)
		},
		"issuer with query": func() error { return p.verifier.Verify(ctx, p.issuer+"?x=1", "chat-ui", p.callback) },
		"http callback":     func() error { return p.verifier.Verify(ctx, p.issuer, "chat-ui", "http://127.0.0.1/oauth2/callback") },
		"callback query":    func() error { return p.verifier.Verify(ctx, p.issuer, "chat-ui", p.callback+"?a=b") },
		"callback fragment": func() error { return p.verifier.Verify(ctx, p.issuer, "chat-ui", p.callback+"#a") },
		"empty client":      func() error { return p.verifier.Verify(ctx, p.issuer, "", p.callback) },
		"zero verifier":     func() error { return (&HTTPVerifier{}).Verify(ctx, p.issuer, "chat-ui", p.callback) },
		"untrusted CA": func() error {
			return NewHTTPVerifier(nil, 5*time.Second).Verify(ctx, p.issuer, "chat-ui", p.callback)
		},
	} {
		if err := call(); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	if p.authHits.Load() != 0 {
		t.Fatalf("rejected inputs still reached the identity provider %d times", p.authHits.Load())
	}
	transport, ok := p.verifier.client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil || p.verifier.client.Jar != nil || p.verifier.client.Timeout <= 0 {
		t.Fatal("verifier must not use proxies or cookies and must have a timeout")
	}
}

func TestHTTPVerifierTimesOut(t *testing.T) {
	p := newProbe(t, 100*time.Millisecond, func(_ *probe, _ http.ResponseWriter, r *http.Request, _ string) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	})
	started := time.Now()
	if err := p.verifier.Verify(context.Background(), p.issuer, "chat-ui", p.callback); err == nil {
		t.Fatal("hung identity provider was accepted")
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("verifier did not honor its timeout")
	}
}
