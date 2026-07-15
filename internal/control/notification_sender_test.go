package control

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/to-alan/vaultmesh/internal/domain"
)

func TestSendGenericWebhookUsesStablePayloadAndConfiguredHeaders(t *testing.T) {
	var received map[string]any
	withNotificationTransport(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer private-token" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("X-Environment"); got != "test" {
			t.Errorf("X-Environment = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		return response(http.StatusAccepted), nil
	}))

	alert := domain.AlertIncident{
		ID: "alt_test", Kind: "backup_failure", ProjectID: "prj_test", ProjectName: "Database",
		Status: "firing", Severity: "critical", Summary: "Backup failed", Description: "Restic exited unsuccessfully.",
		OccurrenceCount: 2, StartedAt: time.Now(), UpdatedAt: time.Now(),
	}
	err := sendNotification(context.Background(), domain.NotificationChannel{Type: "webhook"}, map[string]string{
		"url": "https://alerts.example.com/private-token", "method": "PUT", "authorization": "Bearer private-token",
		"headers": `{"X-Environment":"test"}`,
	}, alert, "repeat")
	if err != nil {
		t.Fatal(err)
	}
	if received["version"] != "1" || received["transition"] != "repeat" {
		t.Fatalf("unexpected webhook envelope: %#v", received)
	}
	if _, ok := received["alert"].(map[string]any); !ok {
		t.Fatalf("alert payload is missing: %#v", received)
	}
}

func TestNotificationHTTPErrorDoesNotLeakEndpoint(t *testing.T) {
	withNotificationTransport(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusUnauthorized), nil
	}))

	endpoint := "https://alerts.example.com/private-token"
	err := sendNotificationHTTP(context.Background(), http.MethodPost, endpoint, nil, nil, "application/json", false)
	if err == nil {
		t.Fatal("expected notification error")
	}
	if strings.Contains(err.Error(), endpoint) || strings.Contains(err.Error(), "private-token") {
		t.Fatalf("notification error leaked endpoint: %v", err)
	}
	if !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("notification error omitted status: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func withNotificationTransport(t *testing.T, transport http.RoundTripper) {
	t.Helper()
	previous := newNotificationHTTPClient
	newNotificationHTTPClient = func(bool) *http.Client {
		return &http.Client{Transport: transport, Timeout: time.Second}
	}
	t.Cleanup(func() { newNotificationHTTPClient = previous })
}

func TestNotificationHTTPRequiresExplicitPrivateNetworkAccess(t *testing.T) {
	received := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		received = true
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	err := sendNotificationHTTP(context.Background(), http.MethodPost, server.URL, nil, nil, "application/json", false)
	if err == nil {
		t.Fatal("private notification endpoint was allowed without explicit opt-in")
	}
	if received {
		t.Fatal("request reached a private endpoint before it was allowed")
	}
	if err := sendNotificationHTTP(context.Background(), http.MethodPost, server.URL, nil, nil, "application/json", true); err != nil {
		t.Fatalf("explicitly allowed private endpoint failed: %v", err)
	}
	if !received {
		t.Fatal("explicitly allowed private endpoint did not receive the request")
	}
}

func TestNotificationAddressPolicyAlwaysBlocksLinkLocal(t *testing.T) {
	if notificationAddressAllowed(net.ParseIP("127.0.0.1"), false) || !notificationAddressAllowed(net.ParseIP("127.0.0.1"), true) {
		t.Fatal("loopback address policy did not honor private-network opt-in")
	}
	if notificationAddressAllowed(net.ParseIP("10.0.0.1"), false) || !notificationAddressAllowed(net.ParseIP("10.0.0.1"), true) {
		t.Fatal("private address policy did not honor private-network opt-in")
	}
	if notificationAddressAllowed(net.ParseIP("169.254.169.254"), false) || notificationAddressAllowed(net.ParseIP("169.254.169.254"), true) {
		t.Fatal("link-local metadata address was allowed")
	}
	if notificationAddressAllowed(net.ParseIP("fd00:ec2::254"), false) || notificationAddressAllowed(net.ParseIP("fd00:ec2::254"), true) {
		t.Fatal("IPv6 metadata address was allowed")
	}
	if notificationAddressAllowed(net.ParseIP("100.100.100.200"), false) || notificationAddressAllowed(net.ParseIP("100.100.100.200"), true) {
		t.Fatal("provider metadata address was allowed")
	}
	if notificationAddressAllowed(net.ParseIP("100.64.0.1"), false) || !notificationAddressAllowed(net.ParseIP("100.64.0.1"), true) {
		t.Fatal("shared private address policy did not honor private-network opt-in")
	}
	if notificationAddressAllowed(net.ParseIP("192.0.2.1"), false) || notificationAddressAllowed(net.ParseIP("192.0.2.1"), true) {
		t.Fatal("reserved documentation address was allowed")
	}
	if !notificationAddressAllowed(net.ParseIP("8.8.8.8"), false) {
		t.Fatal("public unicast address was rejected")
	}
}

func response(status int) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("")),
	}
}
