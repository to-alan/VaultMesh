package agent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/to-alan/vaultmesh/internal/domain"
)

func TestNewClientRejectsUnsafeControlPlaneURLs(t *testing.T) {
	for _, value := range []string{
		"http://example.com",
		"https://user:password@example.com",
		"https://example.com/control",
		"https://example.com?target=other",
		"https://example.com/#fragment",
	} {
		t.Run(value, func(t *testing.T) {
			if _, err := NewClient(value, "test"); err == nil {
				t.Fatalf("unsafe control plane URL %q was accepted", value)
			}
		})
	}
	for _, value := range []string{"http://localhost:8080", "http://127.0.0.1:8080", "http://[::1]:8080", "https://example.com/"} {
		t.Run(value, func(t *testing.T) {
			if _, err := NewClient(value, "test"); err != nil {
				t.Fatalf("valid control plane URL %q was rejected: %v", value, err)
			}
		})
	}
}

func TestClientDoesNotForwardEnrollmentAcrossRedirects(t *testing.T) {
	var redirectedRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirectedRequests.Add(1)
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/capture", http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	client, err := NewClient(redirector.URL, "test")
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Enroll(context.Background(), "one-time-enrollment-token", domain.AgentInfo{Hostname: "host"})
	if err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("expected redirect refusal, got %v", err)
	}
	if redirectedRequests.Load() != 0 {
		t.Fatal("enrollment request was forwarded to the redirect target")
	}
}

func TestClientRejectsAmbiguousAndOversizedJSON(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "multiple values", body: `{"revision":1}{"revision":2}`},
		{name: "oversized", body: `{"revision":1,"padding":"` + strings.Repeat("x", maxControlPlaneResponse) + `"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			client, err := NewClient(server.URL, "test")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Config(context.Background(), "agent-token", 0); err == nil {
				t.Fatal("invalid control plane response was accepted")
			}
		})
	}
}

func TestClientPreservesControlPlaneErrorAndClassifiesReportRejections(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"code":"agent_clock_ahead","message":"run start time is too far in the future"}}`)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "test")
	if err != nil {
		t.Fatal(err)
	}
	err = client.Report(context.Background(), "agent-token", domain.RunReport{})
	var controlError *ControlPlaneError
	if !errors.As(err, &controlError) {
		t.Fatalf("expected typed control plane error, got %T: %v", err, err)
	}
	if controlError.StatusCode != http.StatusBadRequest || controlError.Code != "agent_clock_ahead" {
		t.Fatalf("unexpected control plane error: %#v", controlError)
	}
	if IsPermanentReportError(err) {
		t.Fatal("clock skew rejection must remain retryable")
	}

	tests := []struct {
		name      string
		err       error
		permanent bool
	}{
		{name: "malformed report", err: &ControlPlaneError{StatusCode: 400, Code: "validation_failed"}, permanent: true},
		{name: "legacy clock skew", err: &ControlPlaneError{StatusCode: 400, Code: "validation_failed", Message: "run finish time is too far in the future"}},
		{name: "malformed JSON", err: &ControlPlaneError{StatusCode: 400, Code: "invalid_json"}, permanent: true},
		{name: "inventory too large", err: &ControlPlaneError{StatusCode: 413, Code: "snapshot_inventory_too_large"}, permanent: true},
		{name: "identity conflict", err: &ControlPlaneError{StatusCode: 409, Code: "conflict"}, permanent: true},
		{name: "project removed", err: &ControlPlaneError{StatusCode: 404, Code: "not_found"}, permanent: true},
		{name: "authentication", err: &ControlPlaneError{StatusCode: 401, Code: "unauthorized"}},
		{name: "rate limit", err: &ControlPlaneError{StatusCode: 429, Code: "rate_limited"}},
		{name: "server failure", err: &ControlPlaneError{StatusCode: 500, Code: "internal_error"}},
		{name: "unstructured proxy error", err: &ControlPlaneError{StatusCode: 400}},
		{name: "network failure", err: errors.New("connection refused")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsPermanentReportError(test.err); got != test.permanent {
				t.Fatalf("IsPermanentReportError() = %v, want %v", got, test.permanent)
			}
		})
	}
}
