package agent

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/to-alan/vaultmesh/internal/domain"
)

func TestManagerStopCancelsAndWaitsForActiveOperations(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script")
	}
	directory := t.TempDir()
	startedPath := filepath.Join(directory, "backup-started")
	t.Setenv("FAKE_BACKUP_STARTED", startedPath)
	restic := filepath.Join(directory, "fake-restic")
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  snapshots) printf '%s\\n' '[]'; exit 0;;\n" +
		"  backup) : > \"$FAKE_BACKUP_STARTED\"; exec sleep 30;;\n" +
		"esac\n" +
		"exit 12\n"
	if err := os.WriteFile(restic, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	state, err := OpenState(filepath.Join(directory, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	project := domain.AgentProject{
		Project: domain.Project{
			ID: "prj_shutdown", Sources: []domain.Source{{ID: "src_files", Type: "files", Paths: []string{"/tmp"}, Required: true}},
			// A manual command must start immediately even when scheduled backups
			// have a large anti-thundering-herd jitter.
			Schedule: domain.Schedule{JitterSeconds: 3600, MaxRuntimeSeconds: 60},
		},
		Repository: domain.Repository{ID: "repo_shutdown", URL: "/tmp/repository", Password: "secret"},
	}
	if err := state.SetConfig(domain.AgentConfig{Revision: 1, Projects: []domain.AgentProject{project}}); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(state, NewRunner(restic), domain.AgentIdentity{AgentID: "srv_shutdown"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := manager.Manual(domain.Command{ID: "cmd_shutdown01", ProjectID: project.ID, Type: "backup"}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(startedPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("backup process did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}

	stopped := make(chan struct{})
	go func() {
		manager.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("manager did not wait for the canceled operation to finish")
	}

	pending := state.PendingReports()
	if len(pending) != 1 || pending[0].Status != domain.RunCanceled || pending[0].FinishedAt == nil {
		t.Fatalf("active operation did not reach a persisted canceled state: %#v", pending)
	}
	if err := manager.Manual(domain.Command{ID: "cmd_afterstop", ProjectID: project.ID, Type: "backup"}); err == nil {
		t.Fatal("stopped manager accepted a new operation")
	}
}

func TestManagerForbidsOverlappingOperations(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script")
	}
	directory := t.TempDir()
	startedPath := filepath.Join(directory, "backup-started")
	t.Setenv("FAKE_BACKUP_STARTED", startedPath)
	restic := filepath.Join(directory, "fake-restic")
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  snapshots) printf '%s\\n' '[]'; exit 0;;\n" +
		"  backup) : > \"$FAKE_BACKUP_STARTED\"; exec sleep 30;;\n" +
		"esac\n" +
		"exit 12\n"
	if err := os.WriteFile(restic, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	state, err := OpenState(filepath.Join(directory, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	project := domain.AgentProject{
		Project: domain.Project{
			ID: "prj_forbid", Sources: []domain.Source{{ID: "src_files", Type: "files", Paths: []string{"/tmp"}, Required: true}},
			Schedule: domain.Schedule{ConcurrencyPolicy: "forbid", MaxRuntimeSeconds: 60},
		},
		Repository: domain.Repository{ID: "repo_forbid", URL: "/tmp/repository", Password: "secret"},
	}
	if err := state.SetConfig(domain.AgentConfig{Revision: 1, Projects: []domain.AgentProject{project}}); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(state, NewRunner(restic), domain.AgentIdentity{AgentID: "srv_forbid"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer manager.Stop()

	if err := manager.Manual(domain.Command{ID: "cmd_first", ProjectID: project.ID, Type: "backup"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(startedPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first backup process did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// A second overlapping backup must record a skipped terminal state instead
	// of queueing behind the first run on the repository lock.
	overlappingKey := project.ID + ":backup:2026-01-01T00:00:00Z"
	done := make(chan struct{})
	go func() {
		manager.executeWithKey(project, time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC), overlappingKey, "backup", "", nil, false)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("overlapping operation did not skip quickly; it may be blocked on the repository lock")
	}

	reports := state.PendingReports()
	var skipped *domain.RunReport
	for index := range reports {
		if reports[index].IdempotencyKey == overlappingKey {
			skipped = &reports[index]
		}
	}
	if skipped == nil || skipped.Status != domain.RunSkipped || skipped.ErrorCode != "concurrency_forbidden" || skipped.FinishedAt == nil {
		t.Fatalf("overlapping operation was not recorded as skipped: %#v", skipped)
	}
}
