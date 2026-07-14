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
	admin := domain.AdminAccount{
		Username: "admin-" + suffix, PasswordHash: []byte("hash-" + suffix),
		WebAuthnUserID: []byte("user-handle-" + suffix), SecurityData: []byte("v1:security-" + suffix),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := dataStore.SaveAdminAccount(ctx, admin); err != nil {
		t.Fatal(err)
	}
	loadedAdmin, err := dataStore.GetAdminAccount(ctx)
	if err != nil || loadedAdmin.Username != admin.Username || string(loadedAdmin.SecurityData) != string(admin.SecurityData) {
		t.Fatalf("administrator security data was not persisted: %#v err=%v", loadedAdmin, err)
	}
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
		ID: repositoryID, Provider: "s3_compatible", Name: "Repository " + suffix, URL: "s3:https://example.invalid/bucket",
		SecretCiphertext: []byte("v1:test"), CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	project, err := dataStore.CreateProject(ctx, domain.Project{
		ID: projectID, ServerID: serverID, RepositoryID: repositoryID, Name: "Project", Enabled: true,
		Sources:  []domain.Source{{ID: "src", Type: "files", Paths: []string{"/etc"}, Required: true}},
		Schedule: domain.Schedule{Cron: "0 2 * * *", Timezone: "UTC"}, CreatedAt: now, UpdatedAt: now,
		Policy: domain.ProjectPolicy{
			Backup:       domain.BackupPolicy{OneFileSystem: true, ExcludeIfPresent: []string{".nobackup"}},
			Retention:    domain.RetentionPolicy{Enabled: true, KeepLast: 3, KeepDaily: 7},
			Verification: domain.VerificationPolicy{Mode: "subset", ReadDataSubset: "1%"},
		},
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
	if !config.Projects[0].Policy.Backup.OneFileSystem || config.Projects[0].Policy.Retention.KeepDaily != 7 || config.Projects[0].Policy.Verification.ReadDataSubset != "1%" {
		t.Fatalf("project policy was not persisted: %#v", config.Projects[0].Policy)
	}
	_, err = dataStore.CreateCommand(ctx, domain.Command{
		ID: commandID, ProjectID: projectID, Type: "snapshot_browse", CreatedAt: now,
		Payload: map[string]any{"snapshot_id": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "path": "/etc"},
	})
	if err != nil {
		t.Fatal(err)
	}
	commands, err := dataStore.ClaimCommands(ctx, serverID, now, now.Add(time.Minute), 10)
	if err != nil || len(commands) != 1 {
		t.Fatalf("unexpected commands: %#v err=%v", commands, err)
	}
	if commands[0].Payload["path"] != "/etc" {
		t.Fatalf("command payload was not persisted: %#v", commands[0].Payload)
	}
	const snapshotID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	syncedAt := now.Add(2 * time.Second)
	if err := dataStore.ReplaceProjectSnapshots(ctx, projectID, serverID, []domain.Snapshot{{
		ID: snapshotID, Time: now, Hostname: "integration", Paths: []string{"/etc"},
		Tags: []string{"vaultmesh.project_id=" + projectID}, TotalFiles: 4, TotalBytes: 1024,
	}}, syncedAt); err != nil {
		t.Fatal(err)
	}
	if err := dataStore.ReplaceProjectSnapshots(ctx, projectID, serverID, nil, syncedAt.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	snapshots, err := dataStore.ListSnapshots(ctx, projectID, 10)
	if err != nil || len(snapshots) != 1 || snapshots[0].ID != snapshotID || !snapshots[0].LastSyncedAt.Equal(syncedAt) {
		t.Fatalf("snapshot inventory or stale-write guard failed: %#v err=%v", snapshots, err)
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
