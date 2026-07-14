package control

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
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

	forbidden := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	forbidden.Header.Set("Origin", "https://attacker.example.com")
	forbiddenResponse := httptest.NewRecorder()
	handler.ServeHTTP(forbiddenResponse, forbidden)
	if forbiddenResponse.Code != http.StatusForbidden {
		t.Fatalf("expected forbidden origin to return 403, got %d", forbiddenResponse.Code)
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
