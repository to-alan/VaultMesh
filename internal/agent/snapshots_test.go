package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/to-alan/vaultmesh/internal/domain"
)

const (
	testSnapshotID         = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testTaggedSnapshotID   = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	testProjectID          = "prj_snapshots"
	testProtectedTag       = "vaultmesh.protected=true"
	testProjectSnapshotTag = "vaultmesh.project_id=" + testProjectID
)

func TestRunnerListsProjectSnapshotsAndProtection(t *testing.T) {
	restic, logPath := snapshotRestic(t, `
case "$1" in
  snapshots)
    printf '%s\n' '[{"id":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","time":"2026-07-14T03:04:05Z","hostname":"srv","username":"root","paths":["/etc"],"tags":["vaultmesh.project_id=prj_snapshots","vaultmesh.protected=true"],"summary":{"total_files_processed":7,"total_bytes_processed":42}},{"id":"short","time":"2026-07-14T03:04:05Z"}]'
    ;;
  *) exit 12;;
esac`)

	result := NewRunner(restic).ListSnapshots(context.Background(), "srv", snapshotProject())
	if result.Status != domain.RunSucceeded || result.Stats["snapshot_count"] != 1 {
		t.Fatalf("unexpected snapshot result: %#v", result)
	}
	snapshots, ok := result.Stats["snapshots"].([]domain.Snapshot)
	if !ok || len(snapshots) != 1 {
		t.Fatalf("unexpected inventory: %#v", result.Stats["snapshots"])
	}
	if snapshots[0].ID != testSnapshotID || !snapshots[0].Protected || snapshots[0].TotalFiles != 7 || snapshots[0].TotalBytes != 42 {
		t.Fatalf("unexpected snapshot: %#v", snapshots[0])
	}
	assertLoggedCommand(t, logPath, "snapshots --json --host srv --tag "+testProjectSnapshotTag)
}

func TestRunnerBrowsesOneSnapshotDirectory(t *testing.T) {
	restic, logPath := snapshotRestic(t, `
case "$1" in
  ls)
    printf '%s\n' '{"message_type":"snapshot","id":"ignored"}'
    printf '%s\n' '{"message_type":"node","name":"etc","path":"/etc","type":"dir","mtime":"2026-07-14T03:04:05Z"}'
    printf '%s\n' '{"message_type":"node","name":"hosts","path":"/etc/hosts","type":"file","size":42,"permissions":"-rw-r--r--","mtime":"2026-07-14T03:04:05Z"}'
    printf '%s\n' '{"message_type":"node","name":"nginx","path":"/etc/nginx","type":"dir","mtime":"2026-07-14T03:04:05Z"}'
    ;;
  *) exit 12;;
esac`)

	result := NewRunner(restic).BrowseSnapshot(context.Background(), snapshotProject(), testSnapshotID, "/etc")
	if result.Status != domain.RunSucceeded || result.Stats["entry_count"] != 2 {
		t.Fatalf("unexpected browse result: %#v", result)
	}
	entries, ok := result.Stats["entries"].([]domain.SnapshotEntry)
	if !ok || len(entries) != 2 || entries[0].Path != "/etc/hosts" || entries[1].Type != "dir" {
		t.Fatalf("unexpected entries: %#v", result.Stats["entries"])
	}
	assertLoggedCommand(t, logPath, "ls --json "+testSnapshotID+" /etc")
}

func TestRunnerProtectsSnapshotAndResynchronizesChangedID(t *testing.T) {
	restic, logPath := snapshotRestic(t, `
case "$1" in
  tag) exit 0;;
  snapshots)
    printf '%s\n' '[{"id":"abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789","time":"2026-07-14T03:04:05Z","hostname":"srv","paths":["/etc"],"tags":["vaultmesh.project_id=prj_snapshots","vaultmesh.protected=true"]}]'
    ;;
  *) exit 12;;
esac`)

	result := NewRunner(restic).ProtectSnapshot(context.Background(), "srv", snapshotProject(), testSnapshotID, true)
	if result.Status != domain.RunSucceeded || result.Stats["snapshot_count"] != 1 {
		t.Fatalf("unexpected protect result: %#v", result)
	}
	snapshots := result.Stats["snapshots"].([]domain.Snapshot)
	if snapshots[0].ID != testTaggedSnapshotID || !snapshots[0].Protected {
		t.Fatalf("snapshot inventory was not refreshed: %#v", snapshots)
	}
	assertLoggedCommand(t, logPath, "tag --add "+testProtectedTag+" "+testSnapshotID)
}

func TestRunnerRestoresIntoNewIsolatedDirectoryWithoutOverwrite(t *testing.T) {
	directory := t.TempDir()
	restoreRoot := filepath.Join(directory, "restores")
	restic, logPath := snapshotRestic(t, `
case "$1" in
  restore)
    target=""
    while [ "$#" -gt 0 ]; do
      if [ "$1" = "--target" ]; then shift; target="$1"; fi
      shift
    done
    mkdir -p "$target"
    printf '%s\n' '{"message_type":"summary","total_files":3,"files_restored":3,"files_skipped":0,"total_bytes":99,"bytes_restored":99}'
    ;;
  *) exit 12;;
esac`)

	result := NewRunner(restic).SetRestoreRoot(restoreRoot).RestoreSnapshot(
		context.Background(), "cmd_12345678", snapshotProject(), testSnapshotID, "/etc/hosts",
	)
	if result.Status != domain.RunSucceeded || result.Stats["files_restored"] != int64(3) {
		t.Fatalf("unexpected restore result: %#v", result)
	}
	target := filepath.Join(restoreRoot, "cmd_12345678")
	if result.Stats["restore_target"] != target {
		t.Fatalf("unexpected restore target: %#v", result.Stats["restore_target"])
	}
	assertLoggedCommand(t, logPath, "restore --json --target "+target+" --overwrite never "+testSnapshotID+":/etc/hosts")

	second := NewRunner(restic).SetRestoreRoot(restoreRoot).RestoreSnapshot(
		context.Background(), "cmd_12345678", snapshotProject(), testSnapshotID, "/etc/hosts",
	)
	if second.Status != domain.RunFailed || second.ErrorCode != "restore_target_exists" {
		t.Fatalf("existing target was not rejected: %#v", second)
	}
}

func TestSnapshotOperationsRejectUntrustedIdentifiersAndPaths(t *testing.T) {
	runner := NewRunner("restic").SetRestoreRoot(t.TempDir())
	project := snapshotProject()
	tests := []struct {
		name   string
		result RunResult
		code   string
	}{
		{"short snapshot ID", runner.BrowseSnapshot(context.Background(), project, "latest", "/"), "invalid_snapshot_path"},
		{"relative browse path", runner.BrowseSnapshot(context.Background(), project, testSnapshotID, "etc"), "invalid_snapshot_path"},
		{"invalid command ID", runner.RestoreSnapshot(context.Background(), "../escape", project, testSnapshotID, "/"), "invalid_command_id"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.result.Status != domain.RunFailed || test.result.ErrorCode != test.code {
				t.Fatalf("unexpected result: %#v", test.result)
			}
		})
	}
}

func TestRestoreRejectsFilesystemRootAndSymlinkRoot(t *testing.T) {
	runner := NewRunner("restic").SetRestoreRoot(string(filepath.Separator))
	result := runner.RestoreSnapshot(context.Background(), "cmd_12345678", snapshotProject(), testSnapshotID, "/")
	if result.ErrorCode != "restore_root_invalid" {
		t.Fatalf("filesystem root was not rejected: %#v", result)
	}
	if runtime.GOOS == "windows" {
		return
	}
	directory := t.TempDir()
	target := filepath.Join(directory, "real")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	result = NewRunner("restic").SetRestoreRoot(link).RestoreSnapshot(context.Background(), "cmd_12345678", snapshotProject(), testSnapshotID, "/")
	if result.ErrorCode != "restore_root_invalid" {
		t.Fatalf("symlink root was not rejected: %#v", result)
	}
}

func snapshotProject() domain.AgentProject {
	return domain.AgentProject{
		Project:    domain.Project{ID: testProjectID},
		Repository: domain.Repository{ID: "repo", URL: "/tmp/repository", Password: "secret"},
	}
}

func snapshotRestic(t *testing.T, body string) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script")
	}
	directory := t.TempDir()
	logPath := filepath.Join(directory, "restic.log")
	t.Setenv("FAKE_RESTIC_LOG", logPath)
	script := filepath.Join(directory, "fake-restic")
	contents := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> \"$FAKE_RESTIC_LOG\"\n" + body + "\n"
	if err := os.WriteFile(script, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
	return script, logPath
}

func assertLoggedCommand(t *testing.T, logPath, expected string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		logged, err := os.ReadFile(logPath)
		if err == nil && strings.Contains(string(logged), expected) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("missing command %q in %q (read error: %v)", expected, logged, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
