package enrollment

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/apeirora/showroom-msp-chat-ui/internal/accesspolicy"
)

const defaultProbeTimeout = 10 * time.Second

// HTTPVerifier proves registration of a callback with an unauthenticated,
// session-less OIDC authorization probe (prompt=none). Keycloak validates the
// redirect_uri before anything else: an accepted callback answers with a
// redirect to exactly that callback carrying error=login_required and the same
// state; an unregistered one answers with an error page and never redirects.
// No cookie, token, secret or admin credential is sent or accepted.
type HTTPVerifier struct {
	client *http.Client
}

// NewHTTPVerifier uses verified TLS (roots == nil selects the system pool), no
// environment proxy, a hard timeout, no cookie jar and never follows redirects.
func NewHTTPVerifier(roots *x509.CertPool, timeout time.Duration) *HTTPVerifier {
	if timeout <= 0 {
		timeout = defaultProbeTimeout
	}
	transport := &http.Transport{
		Proxy:                  nil,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
		DisableKeepAlives:      true,
		TLSHandshakeTimeout:    timeout,
		ResponseHeaderTimeout:  timeout,
		MaxResponseHeaderBytes: 64 << 10,
	}
	return &HTTPVerifier{client: &http.Client{
		Transport:     transport,
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func randomValue() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func transportError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err // the URL carries one-time probe parameters; omit it
	}
	return err
}

// Verify implements CallbackVerifier. issuer must be an explicit HTTPS issuer URL.
func (v *HTTPVerifier) Verify(ctx context.Context, issuer, clientID, callback string) error {
	if v == nil || v.client == nil {
		return errors.New("callback verifier must be created with NewHTTPVerifier")
	}
	if accesspolicy.HTTPSURL(issuer, false) != nil || !plainToken(clientID) ||
		accesspolicy.HTTPSURL(callback, false) != nil || strings.ContainsAny(callback, "?#") {
		return errors.New("callback verification requires an https issuer, a client ID and an https callback without query or fragment")
	}
	want, err := url.Parse(callback)
	if err != nil || want.Path == "" {
		return errors.New("invalid callback")
	}
	var state, nonce, pkce string
	for _, target := range []*string{&state, &nonce, &pkce} {
		if *target, err = randomValue(); err != nil {
			return errors.New("cannot create probe parameters")
		}
	}
	challenge := sha256.Sum256([]byte(pkce))

	endpoint, err := url.Parse(issuer)
	if err != nil {
		return errors.New("invalid issuer")
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/protocol/openid-connect/auth"
	endpoint.RawPath = ""
	endpoint.RawQuery = url.Values{
		"client_id":             {clientID},
		"redirect_uri":          {callback},
		"response_type":         {"code"},
		"scope":                 {"openid"},
		"prompt":                {"none"},
		"state":                 {state},
		"nonce":                 {nonce},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method": {"S256"},
	}.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return errors.New("cannot create authorization probe")
	}
	response, err := v.client.Do(request)
	if err != nil {
		return fmt.Errorf("authorization probe failed: %w", transportError(err))
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<16))
	return verifyResponse(response, want, state)
}

func verifyResponse(response *http.Response, want *url.URL, state string) error {
	if response.StatusCode != http.StatusFound && response.StatusCode != http.StatusSeeOther {
		return fmt.Errorf("authorization probe returned HTTP %d instead of a redirect", response.StatusCode)
	}
	rawLocation := response.Header.Get("Location")
	got, err := response.Location()
	if err != nil || strings.Contains(rawLocation, "#") {
		return errors.New("authorization probe returned an invalid redirect")
	}
	if got.Scheme != want.Scheme || got.Host != want.Host || got.Path != want.Path || got.RawPath != "" ||
		got.User != nil || got.Opaque != "" || got.Fragment != "" {
		return errors.New("authorization probe redirected somewhere other than the exact callback")
	}
	query, err := url.ParseQuery(got.RawQuery)
	if err != nil {
		return errors.New("authorization probe returned an invalid redirect")
	}
	for _, forbidden := range []string{"code", "access_token", "id_token", "refresh_token", "token"} {
		if query.Has(forbidden) {
			return errors.New("authorization probe unexpectedly returned credentials")
		}
	}
	if len(query["error"]) != 1 || query["error"][0] != "login_required" {
		return errors.New("authorization probe did not report login_required")
	}
	if len(query["state"]) != 1 || subtle.ConstantTimeCompare([]byte(query["state"][0]), []byte(state)) != 1 {
		return errors.New("authorization probe state mismatch")
	}
	return nil
}
