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
	"testing"
	"time"

	"github.com/to-alan/vaultmesh/internal/domain"
	"github.com/to-alan/vaultmesh/internal/secret"
	"github.com/to-alan/vaultmesh/internal/store/memory"
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
	handler := NewHTTPServer(service, logger, "a-very-long-administrator-token", "").Handler()

	var enrollment domain.EnrollmentResult
	requestJSON(t, handler, http.MethodPost, "/api/v1/servers", "a-very-long-administrator-token",
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
	requestJSON(t, handler, http.MethodPost, "/api/v1/repositories", "a-very-long-administrator-token", domain.Repository{
		ServerID:    identity.AgentID,
		Name:        "MinIO",
		URL:         "s3:http://localhost:9000/backups/server",
		Password:    "repository-password",
		Environment: map[string]string{"AWS_ACCESS_KEY_ID": "vaultmesh", "AWS_SECRET_ACCESS_KEY": "secret"},
	}, http.StatusCreated, &repository)
	if repository.ID == "" || repository.Password != "" || repository.Environment != nil {
		t.Fatalf("repository response leaked or omitted data: %#v", repository)
	}

	var project domain.Project
	requestJSON(t, handler, http.MethodPost, "/api/v1/projects", "a-very-long-administrator-token", domain.Project{
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
		t.Fatalf("repository secret was not delivered to its bound agent")
	}
	var command domain.Command
	requestJSON(t, handler, http.MethodPost, "/api/v1/projects/"+project.ID+"/run", "a-very-long-administrator-token",
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

	var runs struct {
		Items []domain.RunReport `json:"items"`
	}
	requestJSON(t, handler, http.MethodGet, "/api/v1/runs", "a-very-long-administrator-token",
		nil, http.StatusOK, &runs)
	if len(runs.Items) != 2 {
		t.Fatalf("unexpected run list: %#v", runs.Items)
	}
}

func TestAdminAPIRejectsMissingToken(t *testing.T) {
	key, err := secret.ParseKey(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := secret.New(key)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHTTPServer(NewService(memory.New(), sealer), slog.Default(), "a-very-long-administrator-token", "").Handler()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/servers", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.Code)
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
		ServerID: identity.AgentID,
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

func requestJSON(t *testing.T, handler http.Handler, method, path, token string, input any, expectedStatus int, output any) {
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
