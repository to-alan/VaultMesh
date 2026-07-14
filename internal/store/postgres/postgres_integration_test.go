package postgres

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/to-alan/vaultmesh/internal/domain"
)

func TestPostgresVerticalSlice(t *testing.T) {
	databaseURL := os.Getenv("VAULTMESH_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("VAULTMESH_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dataStore, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()
	if err := dataStore.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	serverID := "srv_pg_" + suffix
	projectID := "prj_pg_" + suffix
	repositoryID := "repo_pg_" + suffix
	commandID := "cmd_pg_" + suffix
	now := time.Now().UTC()
	enrollmentHash := sha256.Sum256([]byte("enrollment-" + suffix))
	credentialHash := sha256.Sum256([]byte("credential-" + suffix))
	_, err = dataStore.CreateServer(ctx, domain.Server{ID: serverID, Name: "Postgres integration", CreatedAt: now}, enrollmentHash[:], now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	server, err := dataStore.EnrollAgent(ctx, enrollmentHash[:], credentialHash[:], domain.AgentInfo{Hostname: "integration", OS: "linux", Arch: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	if server.ID != serverID {
		t.Fatalf("unexpected server %s", server.ID)
	}
	if _, err := dataStore.AuthenticateAgent(ctx, credentialHash[:]); err != nil {
		t.Fatal(err)
	}
	_, err = dataStore.CreateRepository(ctx, domain.Repository{
		ID: repositoryID, ServerID: serverID, Name: "Repository", URL: "s3:https://example.invalid/bucket",
		SecretCiphertext: []byte("v1:test"), CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	project, err := dataStore.CreateProject(ctx, domain.Project{
		ID: projectID, ServerID: serverID, RepositoryID: repositoryID, Name: "Project", Enabled: true,
		Sources:  []domain.Source{{ID: "src", Type: "files", Paths: []string{"/etc"}, Required: true}},
		Schedule: domain.Schedule{Cron: "0 2 * * *", Timezone: "UTC"}, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if project.Revision != 1 {
		t.Fatalf("unexpected revision %d", project.Revision)
	}
	config, err := dataStore.DesiredConfig(ctx, serverID)
	if err != nil || config.Revision != 1 || len(config.Projects) != 1 {
		t.Fatalf("unexpected config: %#v err=%v", config, err)
	}
	_, err = dataStore.CreateCommand(ctx, domain.Command{ID: commandID, ProjectID: projectID, Type: "backup", CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	commands, err := dataStore.ClaimCommands(ctx, serverID, now, now.Add(time.Minute), 10)
	if err != nil || len(commands) != 1 {
		t.Fatalf("unexpected commands: %#v err=%v", commands, err)
	}
	report := domain.RunReport{
		ID: "run_pg_" + suffix, IdempotencyKey: "manual:" + commandID, ProjectID: projectID,
		ServerID: serverID, ScheduledAt: now, StartedAt: now, Status: domain.RunRunning,
	}
	if err := dataStore.UpsertRun(ctx, report); err != nil {
		t.Fatal(err)
	}
	finished := now.Add(time.Second)
	report.Status = domain.RunSucceeded
	report.FinishedAt = &finished
	report.SnapshotID = "snapshot"
	if err := dataStore.UpsertRun(ctx, report); err != nil {
		t.Fatal(err)
	}
	commands, err = dataStore.ClaimCommands(ctx, serverID, now.Add(2*time.Minute), now.Add(3*time.Minute), 10)
	if err != nil || len(commands) != 0 {
		t.Fatalf("completed command was reclaimed: %#v err=%v", commands, err)
	}
	runs, err := dataStore.ListRuns(ctx, 10)
	if err != nil || len(runs) == 0 {
		t.Fatalf("run was not persisted: %#v err=%v", runs, err)
	}
}
