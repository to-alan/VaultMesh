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

func TestRunnerParsesSuccessfulResticSummary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script")
	}
	script := filepath.Join(t.TempDir(), "fake-restic")
	contents := "#!/bin/sh\n" +
		"if [ \"$1\" = snapshots ]; then printf '%s\\n' '[]'; exit 0; fi\n" +
		"printf '%s\\n' '{\"message_type\":\"status\",\"seconds_elapsed\":1}'\n" +
		"printf '%s\\n' '{\"message_type\":\"summary\",\"files_new\":2,\"total_files_processed\":2,\"total_bytes_processed\":42,\"data_added\":21,\"total_duration\":0.5,\"snapshot_id\":\"abc123\"}'\n"
	if err := os.WriteFile(script, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(script)
	project := domain.AgentProject{
		Project: domain.Project{
			ID:      "prj_test",
			Sources: []domain.Source{{ID: "src", Type: "files", Paths: []string{"/tmp"}, Required: true}},
		},
		Repository: domain.Repository{ID: "repo", URL: "s3:https://example.invalid/bucket", Password: "super-secret"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := runner.Execute(ctx, "srv_test", project)
	if result.Status != domain.RunSucceeded || result.SnapshotID != "abc123" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestRunnerTreatsResticExitThreeAsPartial(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script")
	}
	script := filepath.Join(t.TempDir(), "fake-restic")
	contents := "#!/bin/sh\n" +
		"if [ \"$1\" = snapshots ]; then printf '%s\\n' '[]'; exit 0; fi\n" +
		"printf '%s\\n' '{\"message_type\":\"summary\",\"snapshot_id\":\"partial123\"}'\n" +
		"printf '%s\\n' 'permission denied for super-secret' >&2\n" +
		"exit 3\n"
	if err := os.WriteFile(script, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
	project := domain.AgentProject{
		Project:    domain.Project{ID: "prj", Sources: []domain.Source{{Type: "files", Paths: []string{"/tmp"}}}},
		Repository: domain.Repository{ID: "repo", URL: "s3:https://example.invalid/bucket", Password: "super-secret"},
	}
	result := NewRunner(script).Execute(context.Background(), "srv", project)
	if result.Status != domain.RunPartial {
		t.Fatalf("expected partial, got %#v", result)
	}
	if result.ErrorMessage == "" || contains(result.ErrorMessage, "super-secret") {
		t.Fatalf("secret was not redacted: %q", result.ErrorMessage)
	}
}

func TestRunnerAppliesBackupRetentionAndVerificationPolicy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script")
	}
	directory := t.TempDir()
	logPath := filepath.Join(directory, "restic.log")
	t.Setenv("FAKE_RESTIC_LOG", logPath)
	restic := filepath.Join(directory, "fake-restic")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> \"$FAKE_RESTIC_LOG\"\n" +
		"case \"$1\" in\n" +
		"  snapshots) printf '%s\\n' '[]';;\n" +
		"  backup) printf '%s\\n' '{\"message_type\":\"summary\",\"snapshot_id\":\"policy123\"}';;\n" +
		"  forget|check) exit 0;;\n" +
		"  *) exit 12;;\n" +
		"esac\n"
	if err := os.WriteFile(restic, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	project := domain.AgentProject{
		Project: domain.Project{
			ID: "prj_policy", Sources: []domain.Source{{Type: "files", Paths: []string{"/tmp"}, Required: true}},
			Policy: domain.ProjectPolicy{
				Backup:       domain.BackupPolicy{OneFileSystem: true, ExcludeCaches: true, ExcludeIfPresent: []string{".nobackup"}, ExcludeLargerThan: "2G"},
				Retention:    domain.RetentionPolicy{Enabled: true, KeepLast: 3, KeepDaily: 7, KeepWeekly: 4, Prune: true},
				Verification: domain.VerificationPolicy{Mode: "subset", ReadDataSubset: "5%"},
			},
		},
		Repository: domain.Repository{ID: "repo", URL: "/tmp/repository", Password: "secret"},
	}
	result := NewRunner(restic).Execute(context.Background(), "srv_policy", project)
	if result.Status != domain.RunSucceeded || result.SnapshotID != "policy123" {
		t.Fatalf("unexpected result: %#v", result)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	commands := string(logged)
	for _, expected := range []string{
		"backup --json --host srv_policy --tag vaultmesh.project_id=prj_policy --one-file-system --exclude-caches --exclude-if-present .nobackup --exclude-larger-than 2G",
		"forget --host srv_policy --tag vaultmesh.project_id=prj_policy --group-by host --keep-last 3 --keep-daily 7 --keep-weekly 4 --keep-tag vaultmesh.protected=true --prune",
		"check --read-data-subset=5%",
	} {
		if !strings.Contains(commands, expected) {
			t.Fatalf("missing command %q in:\n%s", expected, commands)
		}
	}
}

func TestRetentionArgumentsCompileSupportedModes(t *testing.T) {
	tests := []struct {
		name      string
		policy    domain.RetentionPolicy
		expected  []string
		forbidden []string
	}{
		{
			name:      "hard count",
			policy:    domain.RetentionPolicy{Mode: "count", KeepLast: 12},
			expected:  []string{"--group-by host", "--keep-last 12"},
			forbidden: []string{"--keep-daily"},
		},
		{
			name:      "smart",
			policy:    domain.RetentionPolicy{Mode: "smart"},
			expected:  []string{"--keep-within-daily 7d", "--keep-within-weekly 28d", "--keep-within-monthly 1y"},
			forbidden: []string{"--keep-last"},
		},
		{
			name:      "age",
			policy:    domain.RetentionPolicy{Mode: "age", KeepWithin: "6m"},
			expected:  []string{"--keep-within 6m"},
			forbidden: []string{"--keep-monthly"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual := strings.Join(retentionArguments("srv", "prj", test.policy, true), " ")
			for _, expected := range append([]string{"--host srv", "--tag vaultmesh.project_id=prj", "--keep-tag vaultmesh.protected=true", "--dry-run --json"}, test.expected...) {
				if !strings.Contains(actual, expected) {
					t.Fatalf("missing %q in %q", expected, actual)
				}
			}
			for _, forbidden := range test.forbidden {
				if strings.Contains(actual, forbidden) {
					t.Fatalf("unexpected %q in %q", forbidden, actual)
				}
			}
		})
	}
}

func TestRunnerPreviewsRetentionWithoutDeletingSnapshots(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script")
	}
	directory := t.TempDir()
	logPath := filepath.Join(directory, "restic.log")
	t.Setenv("FAKE_RESTIC_LOG", logPath)
	restic := filepath.Join(directory, "fake-restic")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> \"$FAKE_RESTIC_LOG\"\n" +
		"printf '%s\\n' 'repository warning on stderr' >&2\n" +
		"printf '%s\\n' '[{\"host\":\"srv\",\"keep\":[{\"id\":\"a\"},{\"id\":\"b\"}],\"remove\":[{\"id\":\"c\"}]}]'\n"
	if err := os.WriteFile(restic, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	project := domain.AgentProject{
		Project:    domain.Project{ID: "prj", Policy: domain.ProjectPolicy{Retention: domain.RetentionPolicy{Enabled: true, Mode: "count", KeepLast: 2, Prune: true}}},
		Repository: domain.Repository{ID: "repo", URL: "/tmp/repository", Password: "secret"},
	}
	result := NewRunner(restic).PreviewRetention(context.Background(), "srv", project)
	if result.Status != domain.RunSucceeded || result.Stats["snapshots_kept"] != 2 || result.Stats["snapshots_removed"] != 1 {
		t.Fatalf("unexpected preview result: %#v", result)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	command := string(logged)
	if !strings.Contains(command, "forget --host srv --tag vaultmesh.project_id=prj --group-by host --dry-run --json --keep-last 2") {
		t.Fatalf("unexpected preview command: %s", command)
	}
	if strings.Contains(command, "--prune") {
		t.Fatalf("preview must never prune: %s", command)
	}
}

func TestRunnerSeparatesBackupFromRepositoryMaintenance(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script")
	}
	directory := t.TempDir()
	logPath := filepath.Join(directory, "restic.log")
	t.Setenv("FAKE_RESTIC_LOG", logPath)
	restic := filepath.Join(directory, "fake-restic")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> \"$FAKE_RESTIC_LOG\"\n" +
		"case \"$1\" in\n" +
		"  snapshots) printf '%s\\n' '[]';;\n" +
		"  backup) printf '%s\\n' '{\"message_type\":\"summary\",\"snapshot_id\":\"separate123\"}';;\n" +
		"  forget|prune|check) exit 0;;\n" +
		"  *) exit 12;;\n" +
		"esac\n"
	if err := os.WriteFile(restic, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	project := domain.AgentProject{
		Project: domain.Project{
			ID: "prj", Sources: []domain.Source{{Type: "files", Paths: []string{"/tmp"}, Required: true}},
			Policy: domain.ProjectPolicy{
				Retention:    domain.RetentionPolicy{Enabled: true, Mode: "count", KeepLast: 5, Prune: true},
				Verification: domain.VerificationPolicy{Mode: "subset", ReadDataSubset: "5%"},
				Maintenance:  domain.MaintenancePolicy{Separate: true},
			},
		},
		Repository: domain.Repository{ID: "repo", URL: "/tmp/repository", Password: "secret"},
	}
	if result := NewRunner(restic).Execute(context.Background(), "srv", project); result.Status != domain.RunSucceeded {
		t.Fatalf("backup failed: %#v", result)
	}
	logged, _ := os.ReadFile(logPath)
	if strings.Contains(string(logged), "forget ") || strings.Contains(string(logged), "prune ") || strings.Contains(string(logged), "check ") {
		t.Fatalf("backup unexpectedly ran maintenance:\n%s", logged)
	}

	runner := NewRunner(restic)
	for name, result := range map[string]RunResult{
		"retention":    runner.ApplyRetention(context.Background(), "srv", project),
		"prune":        runner.Prune(context.Background(), project),
		"verification": runner.Verify(context.Background(), project),
	} {
		if result.Status != domain.RunSucceeded || result.Stats["operation"] != name {
			t.Fatalf("%s failed: %#v", name, result)
		}
	}
	logged, _ = os.ReadFile(logPath)
	commands := string(logged)
	for _, expected := range []string{
		"forget --host srv --tag vaultmesh.project_id=prj --group-by host --keep-last 5",
		"prune",
		"check --read-data-subset=5%",
	} {
		if !strings.Contains(commands, expected) {
			t.Fatalf("missing maintenance command %q in:\n%s", expected, commands)
		}
	}
}

func TestRunnerKeepsSnapshotWhenRetentionFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script")
	}
	restic := filepath.Join(t.TempDir(), "fake-restic")
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  snapshots) printf '%s\\n' '[]';;\n" +
		"  backup) printf '%s\\n' '{\"message_type\":\"summary\",\"snapshot_id\":\"kept123\"}';;\n" +
		"  forget) printf '%s\\n' 'repository locked' >&2; exit 11;;\n" +
		"esac\n"
	if err := os.WriteFile(restic, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	project := domain.AgentProject{
		Project: domain.Project{ID: "prj", Sources: []domain.Source{{Type: "files", Paths: []string{"/tmp"}}}, Policy: domain.ProjectPolicy{
			Retention: domain.RetentionPolicy{Enabled: true, KeepLast: 2},
		}},
		Repository: domain.Repository{ID: "repo", URL: "/tmp/repository", Password: "secret"},
	}
	result := NewRunner(restic).Execute(context.Background(), "srv", project)
	if result.Status != domain.RunPartial || result.SnapshotID != "kept123" || result.ErrorCode != "retention_failed" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestRunnerCreatesMySQLLogicalDumpBeforeBackup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses POSIX shell scripts")
	}
	directory := t.TempDir()
	restic := filepath.Join(directory, "fake-restic")
	resticScript := "#!/bin/sh\n" +
		"if [ \"$1\" = snapshots ]; then printf '%s\\n' '[]'; exit 0; fi\n" +
		"printf '%s\\n' '{\"message_type\":\"summary\",\"total_files_processed\":1,\"snapshot_id\":\"mysql123\"}'\n"
	if err := os.WriteFile(restic, []byte(resticScript), 0o700); err != nil {
		t.Fatal(err)
	}
	mysqldump := filepath.Join(directory, "fake-mysqldump")
	mysqlScript := "#!/bin/sh\n" +
		"for arg in \"$@\"; do case \"$arg\" in --result-file=*) output=${arg#--result-file=};; esac; done\n" +
		"test -n \"$output\" || exit 9\n" +
		"printf '%s\\n' 'CREATE TABLE test (id INT);' > \"$output\"\n"
	if err := os.WriteFile(mysqldump, []byte(mysqlScript), 0o700); err != nil {
		t.Fatal(err)
	}
	pgDump := filepath.Join(directory, "unused-pg-dump")
	if err := os.WriteFile(pgDump, []byte("#!/bin/sh\nexit 99\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	stagingRoot := filepath.Join(directory, "staging")
	if err := os.Mkdir(stagingRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	project := domain.AgentProject{
		Project: domain.Project{
			ID: "prj_mysql",
			Sources: []domain.Source{{
				ID:       "src_mysql",
				Type:     "mysql",
				Required: true,
				Database: &domain.DatabaseSource{Host: "127.0.0.1", Port: 3306, Username: "backup", Password: "db-secret", Database: "app"},
			}},
		},
		Repository: domain.Repository{ID: "repo", URL: "s3:https://example.invalid/bucket", Password: "repository-secret"},
	}
	result := NewRunnerWithTools(restic, mysqldump, pgDump, "docker", stagingRoot).Execute(context.Background(), "srv", project)
	if result.Status != domain.RunSucceeded || result.SnapshotID != "mysql123" {
		t.Fatalf("unexpected result: %#v", result)
	}
	entries, err := os.ReadDir(stagingRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("staging artifacts were not removed: %#v", entries)
	}
}

func TestRunnerBacksUpDockerMountsAndSanitizedManifest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses POSIX shell scripts")
	}
	directory := t.TempDir()
	volume := filepath.Join(directory, "docker-volume")
	if err := os.Mkdir(volume, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_DOCKER_VOLUME", volume)
	restic := filepath.Join(directory, "fake-restic")
	resticScript := "#!/bin/sh\n" +
		"if [ \"$1\" = snapshots ]; then printf '%s\\n' '[]'; exit 0; fi\n" +
		"found_volume=0; found_manifest=0\n" +
		"for arg in \"$@\"; do\n" +
		"  [ \"$arg\" = \"$FAKE_DOCKER_VOLUME\" ] && found_volume=1\n" +
		"  case \"$arg\" in *.docker.json) found_manifest=1; grep -q 'db-password' \"$arg\" && exit 8;; esac\n" +
		"done\n" +
		"[ \"$found_volume\" = 1 ] && [ \"$found_manifest\" = 1 ] || exit 9\n" +
		"printf '%s\\n' '{\"message_type\":\"summary\",\"snapshot_id\":\"docker123\"}'\n"
	if err := os.WriteFile(restic, []byte(resticScript), 0o700); err != nil {
		t.Fatal(err)
	}
	docker := filepath.Join(directory, "fake-docker")
	dockerScript := "#!/bin/sh\n" +
		"printf '[{\"Id\":\"container-id\",\"Name\":\"/app\",\"Config\":{\"Image\":\"example/app:1\",\"Env\":[\"PASSWORD=db-password\"]},\"State\":{\"Status\":\"running\"},\"Mounts\":[{\"Type\":\"volume\",\"Name\":\"app-data\",\"Source\":\"%s\",\"Destination\":\"/data\",\"RW\":true}]}]\n' \"$FAKE_DOCKER_VOLUME\"\n"
	if err := os.WriteFile(docker, []byte(dockerScript), 0o700); err != nil {
		t.Fatal(err)
	}
	stagingRoot := filepath.Join(directory, "staging")
	project := domain.AgentProject{
		Project: domain.Project{ID: "prj_docker", Sources: []domain.Source{{
			ID: "src_docker", Type: "docker", Required: true,
			Docker: &domain.DockerSource{Containers: []string{"app"}, IncludeVolumes: true},
		}}},
		Repository: domain.Repository{ID: "repo", URL: "s3:https://example.invalid/bucket", Password: "repository-secret"},
	}
	result := NewRunnerWithTools(restic, "mysqldump", "pg_dump", docker, stagingRoot).Execute(context.Background(), "srv", project)
	if result.Status != domain.RunSucceeded || result.SnapshotID != "docker123" {
		t.Fatalf("unexpected result: %#v", result)
	}
	entries, err := os.ReadDir(stagingRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("Docker staging artifacts were not removed: %#v", entries)
	}
}

func TestRunnerInitializesMissingRepositoryBeforeBackup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script")
	}
	directory := t.TempDir()
	marker := filepath.Join(directory, "initialized")
	restic := filepath.Join(directory, "fake-restic")
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  snapshots) if [ -f \"$FAKE_RESTIC_MARKER\" ]; then printf '%s\\n' '[]'; exit 0; else exit 10; fi;;\n" +
		"  init) touch \"$FAKE_RESTIC_MARKER\"; exit 0;;\n" +
		"  backup) test -f \"$FAKE_RESTIC_MARKER\" || exit 11; printf '%s\\n' '{\"message_type\":\"summary\",\"snapshot_id\":\"initialized123\"}';;\n" +
		"esac\n"
	if err := os.WriteFile(restic, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	project := domain.AgentProject{
		Project: domain.Project{ID: "prj", Sources: []domain.Source{{Type: "files", Paths: []string{"/tmp"}}}},
		Repository: domain.Repository{
			ID: "repo", URL: "s3:https://example.invalid/bucket", Password: "secret",
			Environment: map[string]string{"FAKE_RESTIC_MARKER": marker},
		},
	}
	result := NewRunner(restic).Execute(context.Background(), "srv", project)
	if result.Status != domain.RunSucceeded || result.SnapshotID != "initialized123" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("repository was not initialized: %v", err)
	}
}

func TestRunnerPassesValidatedRepositoryOptionsToEveryResticCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script")
	}
	directory := t.TempDir()
	logPath := filepath.Join(directory, "arguments.log")
	t.Setenv("FAKE_RESTIC_LOG", logPath)
	restic := filepath.Join(directory, "fake-restic")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> \"$FAKE_RESTIC_LOG\"\n" +
		"case \" $* \" in\n" +
		"  *' snapshots --json '*) printf '%s\\n' '[]'; exit 0;;\n" +
		"  *' backup '*) printf '%s\\n' '{\"message_type\":\"summary\",\"snapshot_id\":\"options123\"}'; exit 0;;\n" +
		"esac\n" +
		"exit 12\n"
	if err := os.WriteFile(restic, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	project := domain.AgentProject{
		Project: domain.Project{ID: "prj", Sources: []domain.Source{{Type: "files", Paths: []string{"/tmp"}}}},
		Repository: domain.Repository{ID: "repo", URL: "s3:https://example.invalid/bucket", Password: "secret", Options: map[string]string{
			"s3.bucket-lookup": "path",
		}},
	}
	result := NewRunner(restic).Execute(context.Background(), "srv", project)
	if result.Status != domain.RunSucceeded {
		t.Fatalf("unexpected result: %#v", result)
	}
	arguments, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(arguments)
	if !strings.Contains(text, "-o s3.bucket-lookup=path snapshots --json") || !strings.Contains(text, "-o s3.bucket-lookup=path backup") {
		t.Fatalf("repository options were not passed to all Restic commands:\n%s", text)
	}
}

func contains(value, part string) bool {
	for index := 0; index+len(part) <= len(value); index++ {
		if value[index:index+len(part)] == part {
			return true
		}
	}
	return false
}
