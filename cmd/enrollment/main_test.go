package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestFailureReportingDoesNotRevealTransportPayload(t *testing.T) {
	cause := errors.New("private upstream payload")
	err := &passError{stage: "enrollment", cause: cause}
	if strings.Contains(err.Error(), cause.Error()) || failureStage(err) != "enrollment" || !errors.Is(err, cause) {
		t.Fatal("error reporting leaked the API payload or lost classification")
	}
	if failureStage(nil) != "" || failureStage(cause) != "unknown" {
		t.Fatal("unclassified failure exposed its payload")
	}
}

func TestReadinessTracksCompletedPassWithoutAffectingLiveness(t *testing.T) {
	var ready atomic.Bool
	handler := healthHandler(&ready)
	for _, successful := range []bool{false, true, false} {
		ready.Store(successful)
		for _, path := range []string{"/readyz", "/healthz"} {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			expected := http.StatusOK
			if path == "/readyz" && !successful {
				expected = http.StatusServiceUnavailable
			}
			if response.Code != expected {
				t.Fatalf("unexpected health state: %s %d", path, response.Code)
			}
		}
	}
}

func TestInvalidCredentialSelectionFailsBeforeReadingFiles(t *testing.T) {
	for _, opts := range []options{
		{interval: 30 * time.Second},
		{config: "not-read", kcpConfig: "not-read", mspConfig: "not-read", platformConfig: "ambiguous",
			platformInCluster: true, interval: 30 * time.Second},
		{config: "not-read", kcpConfig: "not-read", mspConfig: "not-read", platformConfig: "not-read", interval: time.Second},
	} {
		if err := run(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "explicit configuration") {
			t.Fatal("invalid run was not rejected before IO")
		}
	}
}
