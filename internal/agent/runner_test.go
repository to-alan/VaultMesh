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
		"forget --host srv_policy --tag vaultmesh.project_id=prj_policy --keep-last 3 --keep-daily 7 --keep-weekly 4 --prune",
		"check --read-data-subset=5%",
	} {
		if !strings.Contains(commands, expected) {
			t.Fatalf("missing command %q in:\n%s", expected, commands)
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
