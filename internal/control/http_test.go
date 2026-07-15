package control

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
	"github.com/to-alan/vaultmesh/internal/domain"
	"github.com/to-alan/vaultmesh/internal/secret"
	"github.com/to-alan/vaultmesh/internal/store"
	"github.com/to-alan/vaultmesh/internal/store/memory"
)

const (
	testAdminUsername = "admin"
	testAdminPassword = "correct-horse-battery-staple"
)

func TestControlPlaneVerticalSlice(t *testing.T) {
	dataStore := memory.New()
	key := bytes.Repeat([]byte{7}, 32)
	sealer, err := secret.New(key)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(dataStore, sealer)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := newTestHTTPHandler(t, service, logger, false, []string{"https://console.example.com"})
	adminCookie := loginAdmin(t, handler)

	var enrollment domain.EnrollmentResult
	requestJSONWithCookie(t, handler, http.MethodPost, "/api/v1/servers", adminCookie,
		map[string]string{"name": "Test VPS"}, http.StatusCreated, &enrollment)
	if enrollment.Server.ID == "" || enrollment.EnrollmentToken == "" {
		t.Fatalf("incomplete enrollment response: %#v", enrollment)
	}

	var identity domain.AgentIdentity
	requestJSON(t, handler, http.MethodPost, "/api/v1/enroll", "", map[string]any{
		"enrollment_token": enrollment.EnrollmentToken,
		"hostname":         "test-vps",
		"os":               "linux",
		"arch":             "amd64",
		"agent_version":    "test",
	}, http.StatusCreated, &identity)
	if identity.AgentID != enrollment.Server.ID || identity.Token == "" {
		t.Fatalf("unexpected agent identity: %#v", identity)
	}

	var repository domain.Repository
	requestJSONWithCookie(t, handler, http.MethodPost, "/api/v1/repositories", adminCookie, domain.Repository{
		Provider:    "s3_compatible",
		Name:        "MinIO",
		URL:         "s3:http://localhost:9000/backups/server",
		Password:    "repository-password",
		Environment: map[string]string{"AWS_ACCESS_KEY_ID": "vaultmesh", "AWS_SECRET_ACCESS_KEY": "secret"},
		Options:     map[string]string{"s3.bucket-lookup": "path"},
	}, http.StatusCreated, &repository)
	if repository.ID == "" || repository.Password != "" || repository.Environment != nil || repository.Options != nil {
		t.Fatalf("repository response leaked or omitted data: %#v", repository)
	}

	var project domain.Project
	requestJSONWithCookie(t, handler, http.MethodPost, "/api/v1/projects", adminCookie, domain.Project{
		ServerID:     identity.AgentID,
		RepositoryID: repository.ID,
		Name:         "Configuration",
		Sources: []domain.Source{{
			Type:     "files",
			Paths:    []string{"/etc"},
			Required: true,
		}},
		Schedule: domain.Schedule{Cron: "0 2 * * *", Timezone: "UTC"},
	}, http.StatusCreated, &project)
	if project.Revision != 1 {
		t.Fatalf("unexpected project revision %d", project.Revision)
	}

	var config domain.AgentConfig
	requestJSON(t, handler, http.MethodGet, "/api/v1/agent/config?after=0", identity.Token,
		nil, http.StatusOK, &config)
	if config.Revision != 1 || len(config.Projects) != 1 {
		t.Fatalf("unexpected agent config: %#v", config)
	}
	if config.Projects[0].Repository.Password != "repository-password" {
		t.Fatalf("repository secret was not delivered to the assigned agent")
	}
	if config.Projects[0].Repository.Options["s3.bucket-lookup"] != "path" {
		t.Fatalf("repository backend options were not delivered to the assigned agent: %#v", config.Projects[0].Repository.Options)
	}
	if !strings.HasSuffix(config.Projects[0].Repository.URL, "/"+identity.AgentID) {
		t.Fatalf("repository channel was not scoped to its assigned server: %s", config.Projects[0].Repository.URL)
	}
	var command domain.Command
	requestJSONWithCookie(t, handler, http.MethodPost, "/api/v1/projects/"+project.ID+"/run", adminCookie,
		nil, http.StatusAccepted, &command)
	if command.ID == "" || command.ProjectID != project.ID {
		t.Fatalf("unexpected manual command: %#v", command)
	}
	var commands struct {
		Items []domain.Command `json:"items"`
	}
	requestJSON(t, handler, http.MethodGet, "/api/v1/agent/commands", identity.Token,
		nil, http.StatusOK, &commands)
	if len(commands.Items) != 1 || commands.Items[0].ID != command.ID {
		t.Fatalf("manual command was not leased to its agent: %#v", commands.Items)
	}

	now := time.Now().UTC()
	finished := now.Add(time.Second)
	requestJSON(t, handler, http.MethodPost, "/api/v1/agent/runs", identity.Token, domain.RunReport{
		ID:             "run_test",
		IdempotencyKey: project.ID + ":2026-01-01T00:00:00Z",
		ProjectID:      project.ID,
		ScheduledAt:    now,
		StartedAt:      now,
		FinishedAt:     &finished,
		Status:         domain.RunSucceeded,
		SnapshotID:     "snapshot-test",
	}, http.StatusNoContent, nil)
	requestJSON(t, handler, http.MethodPost, "/api/v1/agent/runs", identity.Token, domain.RunReport{
		ID:             "run_test",
		IdempotencyKey: project.ID + ":different-identity",
		ProjectID:      project.ID,
		ScheduledAt:    now,
		StartedAt:      now,
		FinishedAt:     &finished,
		Status:         domain.RunFailed,
		ErrorMessage:   "must not replace the original run",
	}, http.StatusConflict, nil)
	requestJSON(t, handler, http.MethodPost, "/api/v1/agent/runs", identity.Token, domain.RunReport{
		ID:             "run_test",
		IdempotencyKey: project.ID + ":2026-01-01T00:00:00Z",
		ProjectID:      project.ID,
		ScheduledAt:    now,
		StartedAt:      now,
		Status:         domain.RunRunning,
	}, http.StatusNoContent, nil)
	requestJSON(t, handler, http.MethodPost, "/api/v1/agent/runs", identity.Token, domain.RunReport{
		ID:             "run_manual",
		IdempotencyKey: "manual:" + command.ID,
		ProjectID:      project.ID,
		ScheduledAt:    now,
		StartedAt:      now,
		FinishedAt:     &finished,
		Status:         domain.RunSucceeded,
		SnapshotID:     "snapshot-manual",
	}, http.StatusNoContent, nil)
	commands.Items = nil
	requestJSON(t, handler, http.MethodGet, "/api/v1/agent/commands", identity.Token,
		nil, http.StatusOK, &commands)
	if len(commands.Items) != 0 {
		t.Fatalf("accepted manual command was leased again: %#v", commands.Items)
	}

	var previewCommand domain.Command
	requestJSONWithCookie(t, handler, http.MethodPost, "/api/v1/projects/"+project.ID+"/retention-preview", adminCookie,
		nil, http.StatusAccepted, &previewCommand)
	commands.Items = nil
	requestJSON(t, handler, http.MethodGet, "/api/v1/agent/commands", identity.Token,
		nil, http.StatusOK, &commands)
	if len(commands.Items) != 1 || commands.Items[0].ID != previewCommand.ID || commands.Items[0].Type != "retention_preview" {
		t.Fatalf("retention preview command was not leased to its agent: %#v", commands.Items)
	}
	requestJSON(t, handler, http.MethodPost, "/api/v1/agent/runs", identity.Token, domain.RunReport{
		ID:             "run_retention_preview",
		IdempotencyKey: "manual:" + previewCommand.ID,
		ProjectID:      project.ID,
		ScheduledAt:    now,
		StartedAt:      now,
		FinishedAt:     &finished,
		Status:         domain.RunSucceeded,
		Stats:          map[string]any{"operation": "retention_preview", "snapshots_kept": 2, "snapshots_removed": 1},
	}, http.StatusNoContent, nil)

	var runs struct {
		Items []domain.RunReport `json:"items"`
	}
	requestJSONWithCookie(t, handler, http.MethodGet, "/api/v1/runs", adminCookie,
		nil, http.StatusOK, &runs)
	if len(runs.Items) != 3 {
		t.Fatalf("unexpected run list: %#v", runs.Items)
	}
	for _, report := range runs.Items {
		if report.ID == "run_test" && report.Status != domain.RunSucceeded {
			t.Fatalf("delayed running report regressed terminal run: %#v", report)
		}
	}
	var dashboard domain.Dashboard
	requestJSONWithCookie(t, handler, http.MethodGet, "/api/v1/dashboard", adminCookie,
		nil, http.StatusOK, &dashboard)
	if dashboard.RunsSucceeded != 2 {
		t.Fatalf("maintenance operation polluted backup success metrics: %#v", dashboard)
	}

	var refreshCommand domain.Command
	requestJSONWithCookie(t, handler, http.MethodPost, "/api/v1/projects/"+project.ID+"/snapshots/refresh", adminCookie,
		nil, http.StatusAccepted, &refreshCommand)
	commands.Items = nil
	requestJSON(t, handler, http.MethodGet, "/api/v1/agent/commands", identity.Token,
		nil, http.StatusOK, &commands)
	if len(commands.Items) != 1 || commands.Items[0].Type != "snapshot_sync" {
		t.Fatalf("snapshot refresh command was not leased: %#v", commands.Items)
	}
	const snapshotID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	requestJSON(t, handler, http.MethodPost, "/api/v1/agent/runs", identity.Token, domain.RunReport{
		ID:             "run_snapshot_sync",
		IdempotencyKey: "manual:" + refreshCommand.ID,
		ProjectID:      project.ID,
		ScheduledAt:    now,
		StartedAt:      now,
		FinishedAt:     &finished,
		Status:         domain.RunSucceeded,
		Stats: map[string]any{
			"operation": "snapshot_sync",
			"snapshots": []domain.Snapshot{{
				ID: snapshotID, Time: now, Hostname: "test-vps", Paths: []string{"/etc"},
				Tags: []string{"vaultmesh.project_id=" + project.ID}, TotalFiles: 12, TotalBytes: 4096,
			}},
		},
	}, http.StatusNoContent, nil)
	var snapshots struct {
		Items []domain.Snapshot `json:"items"`
	}
	requestJSONWithCookie(t, handler, http.MethodGet, "/api/v1/snapshots?project_id="+project.ID, adminCookie,
		nil, http.StatusOK, &snapshots)
	if len(snapshots.Items) != 1 || snapshots.Items[0].ID != snapshotID || snapshots.Items[0].ProjectID != project.ID || snapshots.Items[0].ServerID != identity.AgentID {
		t.Fatalf("snapshot inventory was not indexed safely: %#v", snapshots.Items)
	}
	requestJSON(t, handler, http.MethodPost, "/api/v1/agent/runs", identity.Token, domain.RunReport{
		ID:             "run_snapshot_sync",
		IdempotencyKey: "manual:" + refreshCommand.ID,
		ProjectID:      project.ID,
		ScheduledAt:    now,
		StartedAt:      now,
		FinishedAt:     &finished,
		Status:         domain.RunSucceeded,
		Stats:          map[string]any{"operation": "snapshot_sync", "snapshots": []domain.Snapshot{}},
	}, http.StatusNoContent, nil)
	snapshots.Items = nil
	requestJSONWithCookie(t, handler, http.MethodGet, "/api/v1/snapshots?project_id="+project.ID, adminCookie,
		nil, http.StatusOK, &snapshots)
	if len(snapshots.Items) != 1 || snapshots.Items[0].ID != snapshotID {
		t.Fatalf("duplicate run mutated the snapshot index: %#v", snapshots.Items)
	}
	requestJSON(t, handler, http.MethodPost, "/api/v1/agent/runs", identity.Token, domain.RunReport{
		ID:             "run_snapshot_sync",
		IdempotencyKey: project.ID + ":conflicting-snapshot-sync",
		ProjectID:      project.ID,
		ScheduledAt:    now,
		StartedAt:      now,
		FinishedAt:     &finished,
		Status:         domain.RunSucceeded,
		Stats:          map[string]any{"operation": "snapshot_sync", "snapshots": []domain.Snapshot{}},
	}, http.StatusConflict, nil)
	snapshots.Items = nil
	requestJSONWithCookie(t, handler, http.MethodGet, "/api/v1/snapshots?project_id="+project.ID, adminCookie,
		nil, http.StatusOK, &snapshots)
	if len(snapshots.Items) != 1 || snapshots.Items[0].ID != snapshotID {
		t.Fatalf("conflicting run mutated the snapshot index: %#v", snapshots.Items)
	}
	requestJSON(t, handler, http.MethodPost, "/api/v1/agent/runs", identity.Token, domain.RunReport{
		ID:             "run_stale_snapshot_sync",
		IdempotencyKey: project.ID + ":snapshot_sync:stale",
		ProjectID:      project.ID,
		ScheduledAt:    now.Add(-time.Minute),
		StartedAt:      now.Add(-time.Minute),
		FinishedAt:     &now,
		Status:         domain.RunSucceeded,
		Stats:          map[string]any{"operation": "snapshot_sync", "snapshots": []domain.Snapshot{}},
	}, http.StatusNoContent, nil)
	snapshots.Items = nil
	requestJSONWithCookie(t, handler, http.MethodGet, "/api/v1/snapshots?project_id="+project.ID, adminCookie,
		nil, http.StatusOK, &snapshots)
	if len(snapshots.Items) != 1 || snapshots.Items[0].ID != snapshotID {
		t.Fatalf("stale empty inventory replaced a newer snapshot index: %#v", snapshots.Items)
	}

	tests := []struct {
		name         string
		path         string
		body         any
		commandType  string
		payloadKey   string
		payloadValue any
	}{
		{"browse", "/browse", map[string]string{"path": "/etc"}, "snapshot_browse", "path", "/etc"},
		{"protect", "/protect", map[string]bool{"protected": true}, "snapshot_protect", "protected", true},
		{"restore", "/restore", map[string]string{"path": "/etc/hosts"}, "snapshot_restore", "path", "/etc/hosts"},
	}
	for _, test := range tests {
		t.Run("snapshot "+test.name+" command", func(t *testing.T) {
			var issued domain.Command
			requestJSONWithCookie(t, handler, http.MethodPost, "/api/v1/projects/"+project.ID+"/snapshots/"+snapshotID+test.path,
				adminCookie, test.body, http.StatusAccepted, &issued)
			commands.Items = nil
			requestJSON(t, handler, http.MethodGet, "/api/v1/agent/commands", identity.Token,
				nil, http.StatusOK, &commands)
			if len(commands.Items) != 1 || commands.Items[0].ID != issued.ID || commands.Items[0].Type != test.commandType {
				t.Fatalf("unexpected leased command: %#v", commands.Items)
			}
			if commands.Items[0].Payload["snapshot_id"] != snapshotID || commands.Items[0].Payload[test.payloadKey] != test.payloadValue {
				t.Fatalf("command payload was not preserved: %#v", commands.Items[0].Payload)
			}
		})
	}
	requestJSONWithCookie(t, handler, http.MethodPost, "/api/v1/projects/"+project.ID+"/snapshots/"+snapshotID+"/browse", adminCookie,
		map[string]string{"path": "../etc"}, http.StatusUnprocessableEntity, nil)
}

func TestAdminAPIRequiresLoginSessionAndRejectsBearerToken(t *testing.T) {
	key, err := secret.ParseKey(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := secret.New(key)
	if err != nil {
		t.Fatal(err)
	}
	handler := newTestHTTPHandler(t, NewService(memory.New(), sealer), slog.Default(), false, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/servers", nil)
	request.Header.Set("Authorization", "Bearer legacy-administrator-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.Code)
	}
}

func TestAdminLoginUsesProtectedCookieAndLogoutRevokesSession(t *testing.T) {
	sealer, err := secret.New(bytes.Repeat([]byte{2}, 32))
	if err != nil {
		t.Fatal(err)
	}
	handler := newTestHTTPHandler(t, NewService(memory.New(), sealer), slog.Default(), true, nil)
	cookie := loginAdmin(t, handler)
	if cookie.Name != secureSessionCookie || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("unexpected administrator cookie: %#v", cookie)
	}
	if cookie.MaxAge != 0 || !cookie.Expires.IsZero() {
		t.Fatalf("administrator cookie must be non-persistent: %#v", cookie)
	}

	requestJSONWithCookie(t, handler, http.MethodGet, "/api/v1/auth/session", cookie, nil, http.StatusOK, nil)
	requestJSONWithCookie(t, handler, http.MethodPost, "/api/v1/auth/logout", cookie, nil, http.StatusNoContent, nil)
	requestJSONWithCookie(t, handler, http.MethodGet, "/api/v1/auth/session", cookie, nil, http.StatusUnauthorized, nil)
}

func TestAdminLoginSupportsSecureCrossSiteFrontendCookie(t *testing.T) {
	sealer, err := secret.New(bytes.Repeat([]byte{24}, 32))
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewHTTPServer(NewService(memory.New(), sealer), slog.Default(), AdminAuthConfig{
		Username: testAdminUsername, Password: testAdminPassword,
		CookieSecure: true, CookieSameSite: "none",
	}, []string{"https://console.other-site.example"})
	if err != nil {
		t.Fatal(err)
	}
	cookie := loginAdmin(t, server.Handler())
	if cookie.Name != secureSessionCookie || !cookie.Secure || cookie.SameSite != http.SameSiteNoneMode {
		t.Fatalf("unexpected cross-site administrator cookie: %#v", cookie)
	}
}

func TestAdminLoginRejectsInvalidCredentials(t *testing.T) {
	sealer, err := secret.New(bytes.Repeat([]byte{4}, 32))
	if err != nil {
		t.Fatal(err)
	}
	handler := newTestHTTPHandler(t, NewService(memory.New(), sealer), slog.Default(), false, nil)
	requestJSON(t, handler, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"username": testAdminUsername,
		"password": "wrong-password",
	}, http.StatusUnauthorized, nil)
}

func TestAdminLoginRateLimitIsBoundedPerClient(t *testing.T) {
	sealer, err := secret.New(bytes.Repeat([]byte{4}, 32))
	if err != nil {
		t.Fatal(err)
	}
	handler := newTestHTTPHandler(t, NewService(memory.New(), sealer), slog.Default(), false, nil)

	login := func(remoteAddress, password string) *httptest.ResponseRecorder {
		t.Helper()
		data, err := json.Marshal(map[string]string{"username": testAdminUsername, "password": password})
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(data))
		request.RemoteAddr = remoteAddress
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	for attempt := 1; attempt < authFailureLimit; attempt++ {
		if response := login("198.51.100.10:5000", "wrong-password"); response.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: expected 401, got %d", attempt, response.Code)
		}
	}
	blocked := login("198.51.100.10:5000", "wrong-password")
	if blocked.Code != http.StatusTooManyRequests || blocked.Header().Get("Retry-After") == "" {
		t.Fatalf("expected fifth failure to return 429 with Retry-After, got %d and %q", blocked.Code, blocked.Header().Get("Retry-After"))
	}
	if response := login("198.51.100.10:5000", testAdminPassword); response.Code != http.StatusTooManyRequests {
		t.Fatalf("expected blocked client to remain limited, got %d", response.Code)
	}
	if response := login("198.51.100.11:5000", testAdminPassword); response.Code != http.StatusOK {
		t.Fatalf("expected an independent client to log in, got %d", response.Code)
	}
}

func TestAuditTrailRecordsSuccessfulAndFailedSensitiveActions(t *testing.T) {
	sealer, err := secret.New(bytes.Repeat([]byte{10}, 32))
	if err != nil {
		t.Fatal(err)
	}
	handler := newTestHTTPHandler(t, NewService(memory.New(), sealer), slog.Default(), false, nil)
	requestJSON(t, handler, http.MethodPost, "/api/v1/auth/logout", "", nil, http.StatusNoContent, nil)
	adminCookie := loginAdmin(t, handler)

	requestJSONWithCookie(t, handler, http.MethodPost, "/api/v1/servers", adminCookie,
		map[string]string{"name": "Audited server"}, http.StatusCreated, nil)
	requestJSONWithCookie(t, handler, http.MethodPost, "/api/v1/repositories", adminCookie,
		domain.Repository{Name: "Invalid repository", Provider: "unsupported", URL: "invalid"},
		http.StatusUnprocessableEntity, nil)

	var result struct {
		Items []domain.AuditEvent `json:"items"`
	}
	requestJSONWithCookie(t, handler, http.MethodGet, "/api/v1/audit-events?limit=20", adminCookie,
		nil, http.StatusOK, &result)
	assertEvent := func(action, outcome string, status int) domain.AuditEvent {
		t.Helper()
		for _, event := range result.Items {
			if event.Action == action && event.Outcome == outcome && event.StatusCode == status {
				if event.ID == "" || event.Actor == "" || event.ClientIP == "" || event.CreatedAt.IsZero() {
					t.Fatalf("audit event is incomplete: %#v", event)
				}
				return event
			}
		}
		t.Fatalf("missing audit event action=%s outcome=%s status=%d: %#v", action, outcome, status, result.Items)
		return domain.AuditEvent{}
	}
	assertEvent("auth.password", domain.AuditSucceeded, http.StatusOK)
	if event := assertEvent("server.create", domain.AuditSucceeded, http.StatusCreated); event.ResourceID == "" {
		t.Fatalf("created server audit event omitted its resource ID: %#v", event)
	}
	assertEvent("repository.create", domain.AuditFailed, http.StatusUnprocessableEntity)
	for _, event := range result.Items {
		if event.Action == "auth.logout" {
			t.Fatalf("anonymous no-op logout created audit noise: %#v", event)
		}
	}
}

func TestPublicAuditFailureSamplerPreventsWriteAmplification(t *testing.T) {
	sampler := newAuditFailureSampler()
	now := time.Date(2026, time.July, 15, 3, 0, 0, 0, time.UTC)
	if !sampler.allow("auth.password", "198.51.100.40", http.StatusUnauthorized, now) {
		t.Fatal("first public failure was unexpectedly suppressed")
	}
	if sampler.allow("auth.password", "198.51.100.40", http.StatusUnauthorized, now.Add(30*time.Second)) {
		t.Fatal("duplicate public failure was not sampled")
	}
	if !sampler.allow("auth.password", "198.51.100.40", http.StatusTooManyRequests, now.Add(30*time.Second)) {
		t.Fatal("a different HTTP status should have an independent sample")
	}
	if !sampler.allow("auth.password", "198.51.100.40", http.StatusUnauthorized, now.Add(publicAuditFailureSampleWindow)) {
		t.Fatal("public failure sample window did not expire")
	}
}

func TestAuthClientKeyTrustsForwardedAddressOnlyFromLoopback(t *testing.T) {
	proxied := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	proxied.RemoteAddr = "127.0.0.1:4321"
	proxied.Header.Set("X-Forwarded-For", "198.51.100.20, 127.0.0.1")
	if got := authClientKey(proxied); got != "198.51.100.20" {
		t.Fatalf("proxied client key = %q", got)
	}

	direct := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	direct.RemoteAddr = "203.0.113.10:4321"
	direct.Header.Set("X-Forwarded-For", "198.51.100.21")
	if got := authClientKey(direct); got != "203.0.113.10" {
		t.Fatalf("untrusted forwarded address changed client key to %q", got)
	}
}

func TestAuthAttemptLimiterUsesProgressiveLockoutAndSuccessReset(t *testing.T) {
	limiter := newAuthAttemptLimiter()
	clientKey := "198.51.100.30"
	now := time.Date(2026, time.July, 15, 2, 0, 0, 0, time.UTC)
	for attempt := 1; attempt < authFailureLimit; attempt++ {
		if retryAfter, blocked := limiter.recordFailure(clientKey, now); blocked || retryAfter != 0 {
			t.Fatalf("attempt %d was limited early for %s", attempt, retryAfter)
		}
	}
	if retryAfter, blocked := limiter.recordFailure(clientKey, now); !blocked || retryAfter != authInitialLockout {
		t.Fatalf("first lockout = (%s, %v), want (%s, true)", retryAfter, blocked, authInitialLockout)
	}
	if retryAfter, blocked := limiter.retryAfter(clientKey, now.Add(30*time.Second)); !blocked || retryAfter != 30*time.Second {
		t.Fatalf("active lockout = (%s, %v), want (30s, true)", retryAfter, blocked)
	}
	afterFirstLockout := now.Add(authInitialLockout)
	if retryAfter, blocked := limiter.retryAfter(clientKey, afterFirstLockout); blocked || retryAfter != 0 {
		t.Fatalf("expired lockout remained active for %s", retryAfter)
	}
	if retryAfter, blocked := limiter.recordFailure(clientKey, afterFirstLockout); !blocked || retryAfter != 2*authInitialLockout {
		t.Fatalf("second lockout = (%s, %v), want (%s, true)", retryAfter, blocked, 2*authInitialLockout)
	}
	limiter.recordSuccess(clientKey)
	if retryAfter, blocked := limiter.retryAfter(clientKey, afterFirstLockout); blocked || retryAfter != 0 {
		t.Fatalf("successful authentication did not reset limiter: (%s, %v)", retryAfter, blocked)
	}
}

func TestAdministratorPasswordChangePersistsAndRevokesSession(t *testing.T) {
	dataStore := memory.New()
	sealer, err := secret.New(bytes.Repeat([]byte{5}, 32))
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(dataStore, sealer)
	handler := newTestHTTPHandler(t, service, slog.Default(), false, nil)
	cookie := loginAdmin(t, handler)
	newPassword := "a-new-correct-horse-battery-staple"
	requestJSONWithCookie(t, handler, http.MethodPost, "/api/v1/profile/password", cookie, map[string]string{
		"current_password": testAdminPassword,
		"new_password":     newPassword,
	}, http.StatusNoContent, nil)
	requestJSONWithCookie(t, handler, http.MethodGet, "/api/v1/auth/session", cookie, nil, http.StatusUnauthorized, nil)
	requestJSON(t, handler, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"username": testAdminUsername, "password": testAdminPassword,
	}, http.StatusUnauthorized, nil)
	requestJSON(t, handler, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"username": testAdminUsername, "password": newPassword,
	}, http.StatusOK, nil)

	// A new control-plane process must load the persisted account rather than
	// overwriting it with the bootstrap environment password.
	restarted := newTestHTTPHandler(t, service, slog.Default(), false, nil)
	requestJSON(t, restarted, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"username": testAdminUsername, "password": newPassword,
	}, http.StatusOK, nil)
}

func TestTOTPLoginAndOneTimeRecoveryCode(t *testing.T) {
	sealer, err := secret.New(bytes.Repeat([]byte{6}, 32))
	if err != nil {
		t.Fatal(err)
	}
	handler := newTestHTTPHandler(t, NewService(memory.New(), sealer), slog.Default(), false, nil)
	adminCookie := loginAdmin(t, handler)

	var setup struct {
		Secret string `json:"secret"`
	}
	requestJSONWithCookie(t, handler, http.MethodPost, "/api/v1/profile/totp/begin", adminCookie,
		map[string]string{"password": testAdminPassword}, http.StatusOK, &setup)
	if setup.Secret == "" {
		t.Fatal("TOTP setup did not return a secret")
	}
	activationCode, err := totp.GenerateCode(setup.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var enabled struct {
		RecoveryCodes []string `json:"recovery_codes"`
	}
	requestJSONWithCookie(t, handler, http.MethodPost, "/api/v1/profile/totp/enable", adminCookie,
		map[string]string{"code": activationCode}, http.StatusOK, &enabled)
	if len(enabled.RecoveryCodes) != 10 {
		t.Fatalf("expected 10 recovery codes, got %d", len(enabled.RecoveryCodes))
	}
	requestJSONWithCookie(t, handler, http.MethodPost, "/api/v1/auth/logout", adminCookie, nil, http.StatusNoContent, nil)

	mfaCookie := beginMFALogin(t, handler)
	futureCode, err := totp.GenerateCode(setup.Secret, time.Now().Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	requestJSONWithCookie(t, handler, http.MethodPost, "/api/v1/auth/totp", mfaCookie,
		map[string]string{"code": futureCode}, http.StatusOK, nil)

	mfaCookie = beginMFALogin(t, handler)
	requestJSONWithCookie(t, handler, http.MethodPost, "/api/v1/auth/totp", mfaCookie,
		map[string]string{"code": enabled.RecoveryCodes[0]}, http.StatusOK, nil)
	mfaCookie = beginMFALogin(t, handler)
	requestJSONWithCookie(t, handler, http.MethodPost, "/api/v1/auth/totp", mfaCookie,
		map[string]string{"code": enabled.RecoveryCodes[0]}, http.StatusUnauthorized, nil)
}

func TestRecentAuthenticationRequiresSecondFactorWhenTOTPEnabled(t *testing.T) {
	sealer, err := secret.New(bytes.Repeat([]byte{8}, 32))
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewHTTPServer(NewService(memory.New(), sealer), slog.Default(), AdminAuthConfig{
		Username: testAdminUsername, Password: testAdminPassword,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	adminCookie := loginAdmin(t, handler)
	var setup struct {
		Secret string `json:"secret"`
	}
	requestJSONWithCookie(t, handler, http.MethodPost, "/api/v1/profile/totp/begin", adminCookie,
		map[string]string{"password": testAdminPassword}, http.StatusOK, &setup)
	activationCode, err := totp.GenerateCode(setup.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	requestJSONWithCookie(t, handler, http.MethodPost, "/api/v1/profile/totp/enable", adminCookie,
		map[string]string{"code": activationCode}, http.StatusOK, nil)

	server.adminAuth.mu.Lock()
	for token, session := range server.adminAuth.sessions {
		session.AuthenticatedAt = time.Now().Add(-recentAuthTTL - time.Minute)
		server.adminAuth.sessions[token] = session
	}
	server.adminAuth.mu.Unlock()
	requestJSONWithCookie(t, handler, http.MethodPost, "/api/v1/profile/reauthenticate", adminCookie,
		map[string]string{"password": testAdminPassword}, http.StatusUnauthorized, nil)
	futureCode, err := totp.GenerateCode(setup.Secret, time.Now().Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	requestJSONWithCookie(t, handler, http.MethodPost, "/api/v1/profile/reauthenticate", adminCookie,
		map[string]string{"password": testAdminPassword, "code": futureCode}, http.StatusNoContent, nil)
}

func TestSensitiveAccountVerificationIsRateLimitedPerSession(t *testing.T) {
	sealer, err := secret.New(bytes.Repeat([]byte{18}, 32))
	if err != nil {
		t.Fatal(err)
	}
	handler := newTestHTTPHandler(t, NewService(memory.New(), sealer), slog.Default(), false, nil)
	adminCookie := loginAdmin(t, handler)
	for attempt := 1; attempt <= authFailureLimit; attempt++ {
		expected := http.StatusUnauthorized
		if attempt == authFailureLimit {
			expected = http.StatusTooManyRequests
		}
		requestJSONWithCookie(t, handler, http.MethodPost, "/api/v1/profile/reauthenticate", adminCookie,
			map[string]string{"password": "incorrect-password"}, expected, nil)
	}
	requestJSONWithCookie(t, handler, http.MethodPost, "/api/v1/profile/reauthenticate", adminCookie,
		map[string]string{"password": testAdminPassword}, http.StatusTooManyRequests, nil)
}

func TestTOTPSetupIsBoundToTheStartingAdminSession(t *testing.T) {
	sealer, err := secret.New(bytes.Repeat([]byte{19}, 32))
	if err != nil {
		t.Fatal(err)
	}
	handler := newTestHTTPHandler(t, NewService(memory.New(), sealer), slog.Default(), false, nil)
	startingSession := loginAdmin(t, handler)
	otherSession := loginAdmin(t, handler)
	var setup struct {
		Secret string `json:"secret"`
	}
	requestJSONWithCookie(t, handler, http.MethodPost, "/api/v1/profile/totp/begin", startingSession,
		map[string]string{"password": testAdminPassword}, http.StatusOK, &setup)
	code, err := totp.GenerateCode(setup.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	requestJSONWithCookie(t, handler, http.MethodPost, "/api/v1/profile/totp/enable", otherSession,
		map[string]string{"code": code}, http.StatusConflict, nil)
	requestJSONWithCookie(t, handler, http.MethodPost, "/api/v1/profile/totp/enable", startingSession,
		map[string]string{"code": code}, http.StatusOK, nil)
}

func TestSecondFactorStateRollsBackWhenPersistenceFails(t *testing.T) {
	dataStore := memory.New()
	sealer, err := secret.New(bytes.Repeat([]byte{23}, 32))
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(dataStore, sealer)
	server, err := NewHTTPServer(service, slog.Default(), AdminAuthConfig{
		Username: testAdminUsername, Password: testAdminPassword,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	adminCookie := loginAdmin(t, handler)
	var setup struct {
		Secret string `json:"secret"`
	}
	requestJSONWithCookie(t, handler, http.MethodPost, "/api/v1/profile/totp/begin", adminCookie,
		map[string]string{"password": testAdminPassword}, http.StatusOK, &setup)
	activationCode, err := totp.GenerateCode(setup.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	requestJSONWithCookie(t, handler, http.MethodPost, "/api/v1/profile/totp/enable", adminCookie,
		map[string]string{"code": activationCode}, http.StatusOK, nil)

	server.adminAuth.mu.Lock()
	previousStep := server.adminAuth.security.LastTOTPStep
	server.adminAuth.mu.Unlock()
	service.store = &failingAdminSaveStore{Store: dataStore}
	nextCode, err := totp.GenerateCode(setup.Secret, time.Now().Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	requestJSONWithCookie(t, handler, http.MethodPost, "/api/v1/profile/reauthenticate", adminCookie,
		map[string]string{"password": testAdminPassword, "code": nextCode}, http.StatusInternalServerError, nil)
	server.adminAuth.mu.Lock()
	defer server.adminAuth.mu.Unlock()
	if server.adminAuth.security.LastTOTPStep != previousStep {
		t.Fatalf("failed persistence advanced TOTP replay state from %d to %d", previousStep, server.adminAuth.security.LastTOTPStep)
	}
}

func TestPasskeyRegistrationBeginsWithDiscoverableCredentialPolicy(t *testing.T) {
	sealer, err := secret.New(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewHTTPServer(NewService(memory.New(), sealer), slog.Default(), AdminAuthConfig{
		Username: testAdminUsername, Password: testAdminPassword,
		WebAuthnRPID: "localhost", WebAuthnRPOrigins: []string{"http://localhost:3000"},
	}, []string{"http://localhost:3000"})
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	adminCookie := loginAdmin(t, handler)
	var options struct {
		PublicKey struct {
			Challenge              string `json:"challenge"`
			AuthenticatorSelection struct {
				ResidentKey      string `json:"residentKey"`
				UserVerification string `json:"userVerification"`
			} `json:"authenticatorSelection"`
		} `json:"publicKey"`
	}
	requestJSONWithCookie(t, handler, http.MethodPost, "/api/v1/profile/passkeys/register/begin", adminCookie,
		map[string]string{"name": "Test security key"}, http.StatusOK, &options)
	if options.PublicKey.Challenge == "" || options.PublicKey.AuthenticatorSelection.ResidentKey != "required" ||
		options.PublicKey.AuthenticatorSelection.UserVerification != "required" {
		t.Fatalf("unexpected passkey policy: %#v", options)
	}
	server.adminAuth.mu.Lock()
	for token, session := range server.adminAuth.sessions {
		session.AuthenticatedAt = time.Now().Add(-recentAuthTTL - time.Minute)
		server.adminAuth.sessions[token] = session
	}
	server.adminAuth.mu.Unlock()
	requestJSONWithCookie(t, handler, http.MethodPost, "/api/v1/profile/passkeys/register/begin", adminCookie,
		map[string]string{"name": "Second key"}, http.StatusPreconditionRequired, nil)
	requestJSONWithCookie(t, handler, http.MethodPost, "/api/v1/profile/reauthenticate", adminCookie,
		map[string]string{"password": testAdminPassword}, http.StatusNoContent, nil)
	requestJSONWithCookie(t, handler, http.MethodPost, "/api/v1/profile/passkeys/register/begin", adminCookie,
		map[string]string{"name": "Second key"}, http.StatusOK, nil)
}

func beginMFALogin(t *testing.T, handler http.Handler) *http.Cookie {
	t.Helper()
	data, err := json.Marshal(map[string]string{"username": testAdminUsername, "password": testAdminPassword})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(data))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	response := recorder.Result()
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("expected MFA login challenge, got %d: %s", response.StatusCode, body)
	}
	for _, cookie := range response.Cookies() {
		if cookie.Name == localMFACookie || cookie.Name == secureMFACookie {
			return cookie
		}
	}
	t.Fatal("MFA login did not set its opaque challenge cookie")
	return nil
}

func TestCORSAllowsConfiguredOriginAndRejectsOthers(t *testing.T) {
	sealer, err := secret.New(bytes.Repeat([]byte{3}, 32))
	if err != nil {
		t.Fatal(err)
	}
	handler := newTestHTTPHandler(t, NewService(memory.New(), sealer), slog.Default(), false,
		[]string{"https://console.example.com"})

	preflight := httptest.NewRequest(http.MethodOptions, "/api/v1/dashboard", nil)
	preflight.Header.Set("Origin", "https://console.example.com")
	preflight.Header.Set("Access-Control-Request-Method", http.MethodPatch)
	preflightResponse := httptest.NewRecorder()
	handler.ServeHTTP(preflightResponse, preflight)
	if preflightResponse.Code != http.StatusNoContent {
		t.Fatalf("expected allowed preflight to return 204, got %d", preflightResponse.Code)
	}
	if got := preflightResponse.Header().Get("Access-Control-Allow-Origin"); got != "https://console.example.com" {
		t.Fatalf("unexpected access-control-allow-origin %q", got)
	}
	if got := preflightResponse.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("unexpected access-control-allow-credentials %q", got)
	}
	if got := preflightResponse.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, http.MethodPatch) {
		t.Fatalf("PATCH is missing from access-control-allow-methods %q", got)
	}
	if got := preflightResponse.Header().Get("Access-Control-Expose-Headers"); !strings.Contains(got, "Retry-After") {
		t.Fatalf("Retry-After is missing from access-control-expose-headers %q", got)
	}
	if got := preflightResponse.Header().Get("X-VaultMesh-API-Version"); got != "1" {
		t.Fatalf("unexpected API version header %q", got)
	}

	forbidden := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	forbidden.Header.Set("Origin", "https://attacker.example.com")
	forbiddenResponse := httptest.NewRecorder()
	handler.ServeHTTP(forbiddenResponse, forbidden)
	if forbiddenResponse.Code != http.StatusForbidden {
		t.Fatalf("expected forbidden origin to return 403, got %d", forbiddenResponse.Code)
	}
}

func TestJSONEndpointsRejectBrowserSimpleContentTypes(t *testing.T) {
	sealer, err := secret.New(bytes.Repeat([]byte{21}, 32))
	if err != nil {
		t.Fatal(err)
	}
	handler := newTestHTTPHandler(t, NewService(memory.New(), sealer), slog.Default(), false, nil)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"admin","password":"incorrect"}`))
	request.Header.Set("Content-Type", "text/plain")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("simple content type returned %d, want 415", response.Code)
	}
}

func TestControlPlaneNeverServesFrontendDocuments(t *testing.T) {
	sealer, err := secret.New(bytes.Repeat([]byte{22}, 32))
	if err != nil {
		t.Fatal(err)
	}
	handler := newTestHTTPHandler(t, NewService(memory.New(), sealer), slog.Default(), false, nil)

	for _, path := range []string{"/", "/index.html", "/settings/profile", "/api/v1/not-registered"} {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != http.StatusNotFound {
				t.Fatalf("GET %s returned %d, want 404", path, response.Code)
			}
			if got := response.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
				t.Fatalf("GET %s returned non-JSON content type %q", path, got)
			}
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("GET %s returned invalid JSON: %v", path, err)
			}
			if body.Error.Code != "not_found" {
				t.Fatalf("GET %s returned error code %q", path, body.Error.Code)
			}
			if strings.HasPrefix(path, "/api/") {
				if got := response.Header().Get("Cache-Control"); got != "no-store" {
					t.Fatalf("GET %s returned cache policy %q", path, got)
				}
				if got := response.Header().Get("X-VaultMesh-API-Version"); got != "1" {
					t.Fatalf("GET %s returned API version %q", path, got)
				}
			}
		})
	}
}

func TestResponseMetricsWriterCapturesStatusAndBytes(t *testing.T) {
	recorder := httptest.NewRecorder()
	metrics := &responseMetricsWriter{ResponseWriter: recorder}
	written, err := metrics.Write([]byte("response"))
	if err != nil {
		t.Fatal(err)
	}
	metrics.WriteHeader(http.StatusInternalServerError)
	if written != len("response") || metrics.bytes != len("response") || metrics.status != http.StatusOK {
		t.Fatalf("unexpected implicit response metrics: %#v", metrics)
	}

	recorder = httptest.NewRecorder()
	metrics = &responseMetricsWriter{ResponseWriter: recorder}
	metrics.WriteHeader(http.StatusNoContent)
	if metrics.status != http.StatusNoContent || metrics.bytes != 0 {
		t.Fatalf("unexpected explicit response metrics: %#v", metrics)
	}
}

func TestSnapshotSyncTimeBoundsAgentClockSkew(t *testing.T) {
	receivedAt := time.Date(2026, time.July, 15, 1, 2, 3, 0, time.UTC)
	closeFinishedAt := receivedAt.Add(-time.Minute)
	farFuture := receivedAt.Add(24 * time.Hour)
	farPast := receivedAt.Add(-24 * time.Hour)

	for name, test := range map[string]struct {
		finishedAt *time.Time
		want       time.Time
	}{
		"missing":    {want: receivedAt},
		"nearby":     {finishedAt: &closeFinishedAt, want: closeFinishedAt},
		"far future": {finishedAt: &farFuture, want: receivedAt},
		"far past":   {finishedAt: &farPast, want: receivedAt},
	} {
		t.Run(name, func(t *testing.T) {
			if got := snapshotSyncTime(test.finishedAt, receivedAt); !got.Equal(test.want) {
				t.Fatalf("snapshotSyncTime() = %s, want %s", got, test.want)
			}
		})
	}
}

func TestValidateRunReportRejectsMalformedTimelines(t *testing.T) {
	receivedAt := time.Date(2026, time.July, 15, 1, 2, 3, 0, time.UTC)
	validReport := func() domain.RunReport {
		finishedAt := receivedAt.Add(-time.Minute)
		return domain.RunReport{
			ID:             "run_valid",
			IdempotencyKey: "project:2026-07-15T01:00:00Z",
			ProjectID:      "project",
			ScheduledAt:    receivedAt.Add(-3 * time.Minute),
			StartedAt:      receivedAt.Add(-2 * time.Minute),
			FinishedAt:     &finishedAt,
			Status:         domain.RunSucceeded,
		}
	}

	tests := map[string]struct {
		mutate    func(*domain.RunReport)
		wantError bool
	}{
		"valid terminal": {},
		"valid delayed report": {mutate: func(report *domain.RunReport) {
			report.ScheduledAt = receivedAt.Add(-25 * time.Hour)
			report.StartedAt = receivedAt.Add(-24 * time.Hour)
			finishedAt := receivedAt.Add(-24*time.Hour + time.Minute)
			report.FinishedAt = &finishedAt
		}},
		"missing scheduled time": {mutate: func(report *domain.RunReport) {
			report.ScheduledAt = time.Time{}
		}, wantError: true},
		"terminal without finish": {mutate: func(report *domain.RunReport) {
			report.FinishedAt = nil
		}, wantError: true},
		"running with finish": {mutate: func(report *domain.RunReport) {
			report.Status = domain.RunRunning
		}, wantError: true},
		"finish before start": {mutate: func(report *domain.RunReport) {
			finishedAt := report.StartedAt.Add(-time.Second)
			report.FinishedAt = &finishedAt
		}, wantError: true},
		"schedule after start": {mutate: func(report *domain.RunReport) {
			report.ScheduledAt = report.StartedAt.Add(time.Second)
		}, wantError: true},
		"future start": {mutate: func(report *domain.RunReport) {
			report.ScheduledAt = receivedAt.Add(maxAgentClockSkew + time.Minute)
			report.StartedAt = report.ScheduledAt
			finishedAt := report.StartedAt.Add(time.Second)
			report.FinishedAt = &finishedAt
		}, wantError: true},
		"oversized error": {mutate: func(report *domain.RunReport) {
			report.ErrorMessage = strings.Repeat("x", maxRunErrorLength+1)
		}, wantError: true},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			report := validReport()
			if test.mutate != nil {
				test.mutate(&report)
			}
			if err := validateRunReport(report, receivedAt); (err != nil) != test.wantError {
				t.Fatalf("validateRunReport() error = %v, wantError = %v", err, test.wantError)
			}
		})
	}
}

func TestCreateProjectRejectsUnimplementedMissedRunPolicy(t *testing.T) {
	sealer, err := secret.New(bytes.Repeat([]byte{4}, 32))
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewService(memory.New(), sealer).CreateProject(context.Background(), domain.Project{
		ServerID: "srv_test", RepositoryID: "repo_test", Name: "Test",
		Sources:  []domain.Source{{Type: "files", Paths: []string{"/etc"}}},
		Schedule: domain.Schedule{Cron: "0 2 * * *", Timezone: "UTC", MissedRunPolicy: "run_once"},
	})
	if err == nil || !strings.Contains(err.Error(), "supports only skip") {
		t.Fatalf("expected run_once to be rejected explicitly, got %v", err)
	}
}

func TestDatabaseSourcePasswordIsSealedAtRestAndDeliveredOnlyToAgent(t *testing.T) {
	ctx := context.Background()
	dataStore := memory.New()
	sealer, err := secret.New(bytes.Repeat([]byte{8}, 32))
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(dataStore, sealer)
	enrollment, err := service.CreateServer(ctx, "Database host")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := service.EnrollAgent(ctx, enrollment.EnrollmentToken, domain.AgentInfo{Hostname: "db-host", OS: "linux", Arch: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	repository, err := service.CreateRepository(ctx, domain.Repository{
		Provider: "s3_compatible",
		Name:     "Repository",
		URL:      "s3:http://localhost:9000/backups/database",
		Password: "restic-password",
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateProject(ctx, domain.Project{
		ServerID:     identity.AgentID,
		RepositoryID: repository.ID,
		Name:         "MySQL",
		Sources: []domain.Source{{
			Type:     "mysql",
			Required: true,
			Database: &domain.DatabaseSource{Host: "127.0.0.1", Username: "backup", Password: "database-password", Database: "app"},
		}},
		Schedule: domain.Schedule{Cron: "0 3 * * *", Timezone: "UTC"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Sources[0].SecretCiphertext != "" || created.Sources[0].Database.Password != "" {
		t.Fatal("public project response leaked database secret material")
	}
	rawProjects, err := dataStore.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rawProjects) != 1 || rawProjects[0].Sources[0].SecretCiphertext == "" || rawProjects[0].Sources[0].Database.Password != "" {
		t.Fatalf("database password was not replaced with ciphertext at rest: %#v", rawProjects)
	}
	config, err := service.DesiredConfig(ctx, identity.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	got := config.Projects[0].Sources[0]
	if got.SecretCiphertext != "" || got.Database == nil || got.Database.Password != "database-password" {
		t.Fatalf("agent did not receive decrypted database password: %#v", got)
	}
}

func TestUpdateProjectPreservesExistingDatabasePasswordAndAdvancesRevision(t *testing.T) {
	ctx := context.Background()
	dataStore := memory.New()
	sealer, err := secret.New(bytes.Repeat([]byte{10}, 32))
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(dataStore, sealer)
	enrollment, err := service.CreateServer(ctx, "Editable database host")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := service.EnrollAgent(ctx, enrollment.EnrollmentToken, domain.AgentInfo{Hostname: "editable-db", OS: "linux", Arch: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	repository, err := service.CreateRepository(ctx, domain.Repository{
		Provider: "local", Name: "Editable repository", URL: "/tmp/editable-repository", Password: "restic-password",
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateProject(ctx, domain.Project{
		ServerID: identity.AgentID, RepositoryID: repository.ID, Name: "MySQL before edit",
		Sources: []domain.Source{{
			Type: "mysql", Required: true,
			Database: &domain.DatabaseSource{Host: "127.0.0.1", Port: 3306, Username: "backup", Password: "original-secret", Database: "application"},
		}},
		Schedule: domain.Schedule{Cron: "0 2 * * *", Timezone: "UTC"},
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := service.UpdateProject(ctx, created.ID, domain.Project{
		ServerID: identity.AgentID, RepositoryID: repository.ID, Name: "MySQL after edit",
		Sources: []domain.Source{{
			ID: created.Sources[0].ID, Type: "mysql", Required: true,
			Database: &domain.DatabaseSource{Host: "db.internal", Port: 3306, Username: "backup", Database: "application"},
		}},
		Schedule: domain.Schedule{Cron: "30 3 * * *", Timezone: "UTC", GraceSeconds: 1800},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "MySQL after edit" || updated.Revision != 2 || updated.Schedule.GraceSeconds != 1800 {
		t.Fatalf("project edit was not applied: %#v", updated)
	}
	if updated.Sources[0].Database == nil || updated.Sources[0].Database.Password != "" || updated.Sources[0].SecretCiphertext != "" {
		t.Fatalf("public project leaked database secret after edit: %#v", updated.Sources[0])
	}
	config, err := service.DesiredConfig(ctx, identity.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if config.Revision != 2 || len(config.Projects) != 1 || config.Projects[0].Sources[0].Database.Password != "original-secret" {
		t.Fatalf("database password was not preserved for the Agent: %#v", config)
	}
	_, err = service.UpdateProject(ctx, created.ID, domain.Project{
		ServerID: identity.AgentID, RepositoryID: "repo_different", Name: "Move chain",
		Sources: created.Sources, Schedule: created.Schedule,
	})
	if err == nil || !strings.Contains(err.Error(), "recovery chain") {
		t.Fatalf("expected repository move to be rejected, got %v", err)
	}
}

func TestProjectHealthDetectsMissingBackupReports(t *testing.T) {
	ctx := context.Background()
	dataStore := memory.New()
	sealer, err := secret.New(bytes.Repeat([]byte{11}, 32))
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(dataStore, sealer)
	current := time.Date(2026, time.July, 15, 0, 0, 0, 0, time.UTC)
	enrollment, err := service.CreateServer(ctx, "RPO host")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := service.EnrollAgent(ctx, enrollment.EnrollmentToken, domain.AgentInfo{Hostname: "rpo-host"})
	if err != nil {
		t.Fatal(err)
	}
	repository, err := service.CreateRepository(ctx, domain.Repository{Provider: "local", Name: "RPO repository", URL: "/tmp/rpo-repository", Password: "password"})
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return current }
	project, err := service.CreateProject(ctx, domain.Project{
		ServerID: identity.AgentID, RepositoryID: repository.ID, Name: "Hourly evidence",
		Sources: []domain.Source{{Type: "files", Paths: []string{"/tmp"}, Required: true}},
		Schedule: domain.Schedule{
			Cron: "0 1 * * *", Timezone: "UTC", MaxRuntimeSeconds: 3600, GraceSeconds: 1800,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertHealth := func(want string) domain.ProjectHealth {
		t.Helper()
		items, err := service.ProjectHealth(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 1 || items[0].ProjectID != project.ID || items[0].Status != want {
			t.Fatalf("health at %s = %#v, want %s", current, items, want)
		}
		return items[0]
	}
	assertHealth("pending")
	current = time.Date(2026, time.July, 15, 1, 15, 0, 0, time.UTC)
	late := assertHealth("late")
	wantDeadline := time.Date(2026, time.July, 15, 2, 30, 0, 0, time.UTC)
	if late.DeadlineAt == nil || !late.DeadlineAt.Equal(wantDeadline) {
		t.Fatalf("deadline = %v, want %v", late.DeadlineAt, wantDeadline)
	}
	current = wantDeadline.Add(time.Minute)
	assertHealth("overdue")
	dashboard, err := service.Dashboard(ctx, current.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if dashboard.ProjectsOverdue != 1 || dashboard.ProjectsLate != 0 {
		t.Fatalf("dashboard did not include RPO breach: %#v", dashboard)
	}
	started := time.Date(2026, time.July, 15, 1, 20, 0, 0, time.UTC)
	finished := started.Add(10 * time.Minute)
	if err := dataStore.UpsertRun(ctx, domain.RunReport{
		ID: "run_rpo_success", IdempotencyKey: "scheduled:rpo-success", ProjectID: project.ID, ServerID: identity.AgentID,
		ScheduledAt: time.Date(2026, time.July, 15, 1, 0, 0, 0, time.UTC), StartedAt: started, FinishedAt: &finished,
		Status: domain.RunSucceeded, Stats: map[string]any{"operation": "backup"},
	}); err != nil {
		t.Fatal(err)
	}
	assertHealth("healthy")
}

func TestGlobalRepositoryCanBeAssignedToDockerProject(t *testing.T) {
	ctx := context.Background()
	dataStore := memory.New()
	sealer, err := secret.New(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(dataStore, sealer)
	repository, err := service.CreateRepository(ctx, domain.Repository{
		Name: "Global R2", URL: "s3:https://example.r2.cloudflarestorage.com/backups", Password: "restic-password",
	})
	if err != nil {
		t.Fatal(err)
	}
	if repository.Provider != "s3_compatible" {
		t.Fatalf("unexpected default provider %q", repository.Provider)
	}
	enrollment, err := service.CreateServer(ctx, "Docker host")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := service.EnrollAgent(ctx, enrollment.EnrollmentToken, domain.AgentInfo{Hostname: "docker-host", OS: "linux", Arch: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	project, err := service.CreateProject(ctx, domain.Project{
		ServerID: identity.AgentID, RepositoryID: repository.ID, Name: "Docker volumes",
		Sources: []domain.Source{{Type: "docker", Required: true, Docker: &domain.DockerSource{
			Containers: []string{"app", "app"}, IncludeVolumes: true,
		}}},
		Schedule: domain.Schedule{Cron: "0 4 * * *", Timezone: "UTC"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if project.Sources[0].Docker == nil || len(project.Sources[0].Docker.Containers) != 1 {
		t.Fatalf("Docker source was not normalized: %#v", project.Sources[0])
	}
	config, err := service.DesiredConfig(ctx, identity.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Projects) != 1 || config.Projects[0].Repository.ID != repository.ID {
		t.Fatalf("global repository was not delivered to assigned Agent: %#v", config)
	}
}

func TestPublicProjectIncludesNextRunInConfiguredTimezone(t *testing.T) {
	now := time.Date(2026, time.July, 14, 1, 0, 0, 0, time.UTC)
	project := publicProject(domain.Project{
		Enabled:  true,
		Schedule: domain.Schedule{Cron: "30 2 * * *", Timezone: "Asia/Shanghai"},
	}, now)
	want := time.Date(2026, time.July, 14, 18, 30, 0, 0, time.UTC)
	if project.NextRunAt == nil || !project.NextRunAt.Equal(want) {
		t.Fatalf("unexpected next run: got %v, want %v", project.NextRunAt, want)
	}
}

func TestProjectCanBePausedAndResumed(t *testing.T) {
	ctx := context.Background()
	dataStore := memory.New()
	sealer, err := secret.New(bytes.Repeat([]byte{4}, 32))
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(dataStore, sealer)
	enrollment, err := service.CreateServer(ctx, "Pause host")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := service.EnrollAgent(ctx, enrollment.EnrollmentToken, domain.AgentInfo{Hostname: "pause-host"})
	if err != nil {
		t.Fatal(err)
	}
	repository, err := service.CreateRepository(ctx, domain.Repository{Provider: "local", Name: "Pause repo", URL: "/tmp/pause-repo", Password: "password"})
	if err != nil {
		t.Fatal(err)
	}
	project, err := service.CreateProject(ctx, domain.Project{
		ServerID: identity.AgentID, RepositoryID: repository.ID, Name: "Pause me",
		Sources:  []domain.Source{{Type: "files", Paths: []string{"/tmp"}, Required: true}},
		Schedule: domain.Schedule{Cron: "0 2 * * *", Timezone: "UTC"},
	})
	if err != nil {
		t.Fatal(err)
	}
	paused, err := service.SetProjectEnabled(ctx, project.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if paused.Enabled || paused.NextRunAt != nil || paused.Revision != 2 {
		t.Fatalf("unexpected paused project: %#v", paused)
	}
	config, err := service.DesiredConfig(ctx, identity.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if config.Revision != 2 || len(config.Projects) != 0 {
		t.Fatalf("paused project is still in desired config: %#v", config)
	}
	resumed, err := service.SetProjectEnabled(ctx, project.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if !resumed.Enabled || resumed.NextRunAt == nil || resumed.Revision != 3 {
		t.Fatalf("unexpected resumed project: %#v", resumed)
	}
}

func TestRetentionModesAreValidatedAndNormalized(t *testing.T) {
	tests := []struct {
		name      string
		policy    domain.RetentionPolicy
		wantError bool
	}{
		{name: "count", policy: domain.RetentionPolicy{Enabled: true, Mode: "count", KeepLast: 10}},
		{name: "smart", policy: domain.RetentionPolicy{Enabled: true, Mode: "smart"}},
		{name: "gfs", policy: domain.RetentionPolicy{Enabled: true, Mode: "gfs", KeepDaily: 7, KeepMonthly: 12}},
		{name: "age", policy: domain.RetentionPolicy{Enabled: true, Mode: "age", KeepWithin: "1y6m"}},
		{name: "count needs limit", policy: domain.RetentionPolicy{Enabled: true, Mode: "count"}, wantError: true},
		{name: "age needs duration", policy: domain.RetentionPolicy{Enabled: true, Mode: "age", KeepWithin: "90 days"}, wantError: true},
		{name: "unknown mode", policy: domain.RetentionPolicy{Enabled: true, Mode: "custom", KeepLast: 1}, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := domain.ProjectPolicy{Retention: test.policy}
			err := validateProjectPolicy(&policy)
			if (err != nil) != test.wantError {
				t.Fatalf("validateProjectPolicy() error = %v, wantError = %v", err, test.wantError)
			}
		})
	}

	legacy := domain.ProjectPolicy{Retention: domain.RetentionPolicy{Enabled: true, KeepLast: 3, KeepDaily: 7}}
	if err := validateProjectPolicy(&legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.Retention.Mode != "gfs" {
		t.Fatalf("legacy retention mode was not normalized: %#v", legacy.Retention)
	}

	separate := domain.ProjectPolicy{
		Retention:    domain.RetentionPolicy{Enabled: true, Mode: "count", KeepLast: 5, Prune: true},
		Verification: domain.VerificationPolicy{Mode: "metadata"},
		Maintenance: domain.MaintenancePolicy{
			Separate: true, Timezone: "Asia/Shanghai", RetentionCron: "30 3 * * *", PruneCron: "0 4 * * 0", VerificationCron: "0 5 * * 0",
		},
	}
	if err := validateProjectPolicy(&separate); err != nil {
		t.Fatal(err)
	}
	if err := validateMaintenancePolicy(&separate); err != nil {
		t.Fatal(err)
	}
	separate.Maintenance.PruneCron = "not a cron"
	if err := validateMaintenancePolicy(&separate); err == nil {
		t.Fatal("invalid prune maintenance schedule was accepted")
	}
}

func TestNotificationChannelHTTPFlowKeepsSecretsWriteOnly(t *testing.T) {
	dataStore := memory.New()
	sealer, err := secret.New(bytes.Repeat([]byte{23}, 32))
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(dataStore, sealer)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := newTestHTTPHandler(t, service, logger, false, []string{"https://console.example.com"})
	adminCookie := loginAdmin(t, handler)

	var created domain.NotificationChannel
	requestJSONWithCookie(t, handler, http.MethodPost, "/api/v1/notification-channels", adminCookie, domain.NotificationChannel{
		Name: "Operations webhook", Type: "webhook", Enabled: true, SendResolved: true,
		RepeatIntervalSeconds: 14400, EventTypes: []string{"backup_failure", "rpo_overdue"},
		Config: map[string]string{
			"url": "https://alerts.example.com/private-token", "method": "POST",
			"authorization": "Bearer private-authorization", "headers": `{"X-Environment":"test"}`,
		},
	}, http.StatusCreated, &created)
	if created.ID == "" || !created.Configured || created.Destination != "alerts.example.com" {
		t.Fatalf("unexpected public notification channel: %#v", created)
	}
	if created.Config["url"] != "" || created.Config["authorization"] != "" || created.Config["headers"] != "" {
		t.Fatalf("notification channel response leaked a secret: %#v", created.Config)
	}

	var testedURL string
	service.notificationSender = func(_ context.Context, _ domain.NotificationChannel, config map[string]string, _ domain.AlertIncident, _ string) error {
		testedURL = config["url"]
		return nil
	}
	requestJSONWithCookie(t, handler, http.MethodPost, "/api/v1/notification-channels/"+created.ID+"/test", adminCookie,
		nil, http.StatusNoContent, nil)
	if testedURL != "https://alerts.example.com/private-token" {
		t.Fatalf("test notification did not receive decrypted configuration: %q", testedURL)
	}

	created.Name = "Renamed operations webhook"
	created.Config = nil
	var updated domain.NotificationChannel
	requestJSONWithCookie(t, handler, http.MethodPut, "/api/v1/notification-channels/"+created.ID, adminCookie,
		created, http.StatusOK, &updated)
	if updated.Name != created.Name || updated.Destination != "alerts.example.com" {
		t.Fatalf("blank secret update did not preserve configuration: %#v", updated)
	}

	var disabled domain.NotificationChannel
	requestJSONWithCookie(t, handler, http.MethodPatch, "/api/v1/notification-channels/"+created.ID, adminCookie,
		map[string]bool{"enabled": false}, http.StatusOK, &disabled)
	if disabled.Enabled {
		t.Fatal("notification channel remained enabled")
	}
	requestJSONWithCookie(t, handler, http.MethodDelete, "/api/v1/notification-channels/"+created.ID, adminCookie,
		nil, http.StatusNoContent, nil)
	var listed struct {
		Items []domain.NotificationChannel `json:"items"`
	}
	requestJSONWithCookie(t, handler, http.MethodGet, "/api/v1/notification-channels", adminCookie,
		nil, http.StatusOK, &listed)
	if len(listed.Items) != 0 {
		t.Fatalf("archived notification channel was still listed: %#v", listed.Items)
	}
}

func requestJSON(t *testing.T, handler http.Handler, method, path, token string, input any, expectedStatus int, output any) {
	requestJSONWithAuth(t, handler, method, path, token, nil, input, expectedStatus, output)
}

func requestJSONWithCookie(t *testing.T, handler http.Handler, method, path string, cookie *http.Cookie, input any, expectedStatus int, output any) {
	requestJSONWithAuth(t, handler, method, path, "", cookie, input, expectedStatus, output)
}

func requestJSONWithAuth(t *testing.T, handler http.Handler, method, path, token string, cookie *http.Cookie, input any, expectedStatus int, output any) {
	t.Helper()
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(context.Background(), method, path, body)
	if err != nil {
		t.Fatal(err)
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	response := recorder.Result()
	defer response.Body.Close()
	if response.StatusCode != expectedStatus {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("%s %s: expected %d, got %d: %s", method, path, expectedStatus, response.StatusCode, data)
	}
	if output != nil {
		if err := json.NewDecoder(response.Body).Decode(output); err != nil {
			t.Fatal(err)
		}
	}
}

func newTestHTTPHandler(t *testing.T, service *Service, logger *slog.Logger, cookieSecure bool, origins []string) http.Handler {
	t.Helper()
	server, err := NewHTTPServer(service, logger, AdminAuthConfig{
		Username:     testAdminUsername,
		Password:     testAdminPassword,
		CookieSecure: cookieSecure,
	}, origins)
	if err != nil {
		t.Fatal(err)
	}
	return server.Handler()
}

type failingAdminSaveStore struct {
	store.Store
}

func (*failingAdminSaveStore) SaveAdminAccount(context.Context, domain.AdminAccount) error {
	return errors.New("injected administrator persistence failure")
}

func loginAdmin(t *testing.T, handler http.Handler) *http.Cookie {
	t.Helper()
	data, err := json.Marshal(map[string]string{
		"username": testAdminUsername,
		"password": testAdminPassword,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(data))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	response := recorder.Result()
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("login: expected 200, got %d: %s", response.StatusCode, body)
	}
	cookies := response.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("login: expected one session cookie, got %d", len(cookies))
	}
	return cookies[0]
}
