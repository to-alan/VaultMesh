package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/to-alan/vaultmesh/internal/domain"
)

type RunResult struct {
	Status       string
	SnapshotID   string
	ErrorCode    string
	ErrorMessage string
	Stats        map[string]any
}

type Runner struct {
	resticPath    string
	mysqlDumpPath string
	pgDumpPath    string
	dockerPath    string
	stagingRoot   string
}

func NewRunner(resticPath string) *Runner {
	return &Runner{resticPath: resticPath, mysqlDumpPath: "mysqldump", pgDumpPath: "pg_dump", dockerPath: "docker"}
}

func NewRunnerWithTools(resticPath, mysqlDumpPath, pgDumpPath, dockerPath, stagingRoot string) *Runner {
	return &Runner{
		resticPath:    resticPath,
		mysqlDumpPath: mysqlDumpPath,
		pgDumpPath:    pgDumpPath,
		dockerPath:    dockerPath,
		stagingRoot:   stagingRoot,
	}
}

func (r *Runner) Execute(ctx context.Context, agentID string, project domain.AgentProject) RunResult {
	if result := r.ensureRepository(ctx, project.Repository); result != nil {
		return *result
	}
	paths, excludes, cleanup, err := r.prepareSources(ctx, project.Sources)
	if err != nil {
		return RunResult{Status: domain.RunFailed, ErrorCode: "source_preparation_failed", ErrorMessage: redact(truncate(err.Error(), 4096), project.Repository)}
	}
	defer cleanup()
	args := []string{"backup", "--json", "--host", agentID, "--tag", "vaultmesh.project_id=" + project.ID}
	if project.Policy.Backup.OneFileSystem {
		args = append(args, "--one-file-system")
	}
	if project.Policy.Backup.ExcludeCaches {
		args = append(args, "--exclude-caches")
	}
	for _, marker := range project.Policy.Backup.ExcludeIfPresent {
		args = append(args, "--exclude-if-present", marker)
	}
	if project.Policy.Backup.ExcludeLargerThan != "" {
		args = append(args, "--exclude-larger-than", project.Policy.Backup.ExcludeLargerThan)
	}
	for _, pattern := range excludes {
		args = append(args, "--exclude", pattern)
	}
	args = append(args, paths...)
	args = resticArguments(project.Repository, args...)

	command := exec.CommandContext(ctx, r.resticPath, args...)
	command.Env = repositoryEnvironment(project.Repository)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return RunResult{Status: domain.RunFailed, ErrorCode: "process_setup_failed", ErrorMessage: err.Error()}
	}
	var stderr limitedBuffer
	stderr.limit = 16 << 10
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return RunResult{Status: domain.RunFailed, ErrorCode: "restic_not_started", ErrorMessage: redact(err.Error(), project.Repository)}
	}

	summary, parseError := parseResticOutput(stdout)
	waitError := command.Wait()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return RunResult{Status: domain.RunTimedOut, ErrorCode: "max_runtime_exceeded", ErrorMessage: "restic exceeded the configured maximum runtime"}
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return RunResult{Status: domain.RunCanceled, ErrorCode: "canceled", ErrorMessage: "backup was canceled"}
	}

	errorMessage := strings.TrimSpace(stderr.String())
	if parseError != nil && errorMessage == "" {
		errorMessage = parseError.Error()
	}
	errorMessage = redact(truncate(errorMessage, 4096), project.Repository)
	exitCode := 0
	if waitError != nil {
		var exitError *exec.ExitError
		if errors.As(waitError, &exitError) {
			exitCode = exitError.ExitCode()
		} else {
			return RunResult{Status: domain.RunFailed, ErrorCode: "restic_wait_failed", ErrorMessage: redact(waitError.Error(), project.Repository)}
		}
	}
	stats := map[string]any{
		"operation":             "backup",
		"files_new":             summary.FilesNew,
		"files_changed":         summary.FilesChanged,
		"files_unmodified":      summary.FilesUnmodified,
		"total_files_processed": summary.TotalFilesProcessed,
		"total_bytes_processed": summary.TotalBytesProcessed,
		"data_added":            summary.DataAdded,
		"duration_seconds":      summary.TotalDuration,
		"restic_exit_code":      exitCode,
	}
	if exitCode == 3 {
		if errorMessage == "" {
			errorMessage = "restic created an incomplete snapshot because some source data could not be read"
		}
		return RunResult{Status: domain.RunPartial, SnapshotID: summary.SnapshotID, ErrorCode: "source_data_incomplete", ErrorMessage: errorMessage, Stats: stats}
	}
	if exitCode != 0 {
		if errorMessage == "" {
			errorMessage = "restic exited with status " + strconv.Itoa(exitCode)
		}
		return RunResult{Status: domain.RunFailed, ErrorCode: "restic_failed", ErrorMessage: errorMessage, Stats: stats}
	}
	if parseError != nil {
		return RunResult{Status: domain.RunFailed, ErrorCode: "invalid_restic_output", ErrorMessage: errorMessage, Stats: stats}
	}
	if summary.SnapshotID == "" {
		return RunResult{Status: domain.RunFailed, ErrorCode: "snapshot_missing", ErrorMessage: "restic did not return a snapshot ID", Stats: stats}
	}
	if !project.Policy.Maintenance.Separate {
		if result := r.applyPostBackupPolicy(ctx, agentID, project, summary.SnapshotID, stats); result != nil {
			return *result
		}
	}
	return RunResult{Status: domain.RunSucceeded, SnapshotID: summary.SnapshotID, Stats: stats}
}

// PreviewRetention asks Restic to evaluate the exact repository policy without
// deleting snapshots or reclaiming data. It intentionally bypasses repository
// initialization: previewing a missing repository must be a read-only failure.
func (r *Runner) PreviewRetention(ctx context.Context, agentID string, project domain.AgentProject) RunResult {
	stats := map[string]any{"operation": "retention_preview", "dry_run": true}
	if !project.Policy.Retention.Enabled {
		return RunResult{Status: domain.RunFailed, ErrorCode: "retention_disabled", ErrorMessage: "retention is disabled for this project", Stats: stats}
	}
	args := retentionArguments(agentID, project.ID, project.Policy.Retention, true)
	exitCode, kept, removed, errorOutput, err, parseErr := runForgetPreview(ctx, repositoryEnvironment(project.Repository), r.resticPath, resticArguments(project.Repository, args...)...)
	stats["restic_exit_code"] = exitCode
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return RunResult{Status: domain.RunTimedOut, ErrorCode: "max_runtime_exceeded", ErrorMessage: "retention preview exceeded the configured maximum runtime", Stats: stats}
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return RunResult{Status: domain.RunCanceled, ErrorCode: "canceled", ErrorMessage: "retention preview was canceled", Stats: stats}
	}
	if err != nil || exitCode != 0 {
		if errorOutput == "" && err != nil {
			errorOutput = err.Error()
		}
		return RunResult{Status: domain.RunFailed, ErrorCode: "retention_preview_failed", ErrorMessage: redact(truncate(errorOutput, 4096), project.Repository), Stats: stats}
	}
	if parseErr != nil {
		return RunResult{Status: domain.RunFailed, ErrorCode: "invalid_retention_preview", ErrorMessage: parseErr.Error(), Stats: stats}
	}
	stats["snapshots_kept"] = kept
	stats["snapshots_removed"] = removed
	return RunResult{Status: domain.RunSucceeded, Stats: stats}
}

func (r *Runner) ApplyRetention(ctx context.Context, agentID string, project domain.AgentProject) RunResult {
	stats := map[string]any{"operation": "retention"}
	if !project.Policy.Retention.Enabled {
		return RunResult{Status: domain.RunFailed, ErrorCode: "retention_disabled", ErrorMessage: "retention is disabled for this project", Stats: stats}
	}
	args := retentionArguments(agentID, project.ID, project.Policy.Retention, false)
	return r.runMaintenanceCommand(ctx, project.Repository, args, "retention", "retention_failed", stats)
}

func (r *Runner) Prune(ctx context.Context, project domain.AgentProject) RunResult {
	stats := map[string]any{"operation": "prune"}
	if !project.Policy.Retention.Prune {
		return RunResult{Status: domain.RunFailed, ErrorCode: "prune_disabled", ErrorMessage: "prune is disabled for this project", Stats: stats}
	}
	return r.runMaintenanceCommand(ctx, project.Repository, []string{"prune"}, "prune", "prune_failed", stats)
}

func (r *Runner) Verify(ctx context.Context, project domain.AgentProject) RunResult {
	verification := project.Policy.Verification
	stats := map[string]any{"operation": "verification", "verification_mode": verification.Mode}
	if verification.Mode == "" || verification.Mode == "off" {
		return RunResult{Status: domain.RunFailed, ErrorCode: "verification_disabled", ErrorMessage: "repository verification is disabled for this project", Stats: stats}
	}
	args := []string{"check"}
	switch verification.Mode {
	case "subset":
		args = append(args, "--read-data-subset="+verification.ReadDataSubset)
	case "full":
		args = append(args, "--read-data")
	}
	return r.runMaintenanceCommand(ctx, project.Repository, args, "verification", "repository_verification_failed", stats)
}

func (r *Runner) runMaintenanceCommand(ctx context.Context, repository domain.Repository, args []string, operation, errorCode string, stats map[string]any) RunResult {
	exitCode, output, err := runCommand(ctx, repositoryEnvironment(repository), r.resticPath, resticArguments(repository, args...)...)
	stats["restic_exit_code"] = exitCode
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return RunResult{Status: domain.RunTimedOut, ErrorCode: "max_runtime_exceeded", ErrorMessage: operation + " exceeded the configured maximum runtime", Stats: stats}
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return RunResult{Status: domain.RunCanceled, ErrorCode: "canceled", ErrorMessage: operation + " was canceled", Stats: stats}
	}
	if err != nil || exitCode != 0 {
		if output == "" && err != nil {
			output = err.Error()
		}
		return RunResult{Status: domain.RunFailed, ErrorCode: errorCode, ErrorMessage: redact(truncate(output, 4096), repository), Stats: stats}
	}
	return RunResult{Status: domain.RunSucceeded, Stats: stats}
}

func (r *Runner) applyPostBackupPolicy(ctx context.Context, agentID string, project domain.AgentProject, snapshotID string, stats map[string]any) *RunResult {
	environment := repositoryEnvironment(project.Repository)
	retention := project.Policy.Retention
	if retention.Enabled {
		args := retentionArguments(agentID, project.ID, retention, false)
		if retention.Prune {
			args = append(args, "--prune")
		}
		exitCode, output, err := runCommand(ctx, environment, r.resticPath, resticArguments(project.Repository, args...)...)
		stats["retention_applied"] = exitCode == 0 && err == nil
		stats["prune_requested"] = retention.Prune
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return &RunResult{Status: domain.RunTimedOut, SnapshotID: snapshotID, ErrorCode: "max_runtime_exceeded", ErrorMessage: "retention maintenance exceeded the configured maximum runtime", Stats: stats}
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return &RunResult{Status: domain.RunCanceled, SnapshotID: snapshotID, ErrorCode: "canceled", ErrorMessage: "retention maintenance was canceled", Stats: stats}
		}
		if err != nil || exitCode != 0 {
			if output == "" && err != nil {
				output = err.Error()
			}
			return &RunResult{
				Status: domain.RunPartial, SnapshotID: snapshotID, ErrorCode: "retention_failed",
				ErrorMessage: redact(truncate(output, 4096), project.Repository), Stats: stats,
			}
		}
	}

	verification := project.Policy.Verification
	if verification.Mode == "" || verification.Mode == "off" {
		return nil
	}
	args := []string{"check"}
	switch verification.Mode {
	case "subset":
		args = append(args, "--read-data-subset="+verification.ReadDataSubset)
	case "full":
		args = append(args, "--read-data")
	}
	exitCode, output, err := runCommand(ctx, environment, r.resticPath, resticArguments(project.Repository, args...)...)
	stats["verification_mode"] = verification.Mode
	stats["verification_passed"] = exitCode == 0 && err == nil
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &RunResult{Status: domain.RunTimedOut, SnapshotID: snapshotID, ErrorCode: "max_runtime_exceeded", ErrorMessage: "repository verification exceeded the configured maximum runtime", Stats: stats}
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return &RunResult{Status: domain.RunCanceled, SnapshotID: snapshotID, ErrorCode: "canceled", ErrorMessage: "repository verification was canceled", Stats: stats}
	}
	if err != nil || exitCode != 0 {
		if output == "" && err != nil {
			output = err.Error()
		}
		return &RunResult{
			Status: domain.RunPartial, SnapshotID: snapshotID, ErrorCode: "repository_verification_failed",
			ErrorMessage: redact(truncate(output, 4096), project.Repository), Stats: stats,
		}
	}
	return nil
}

func retentionArguments(agentID, projectID string, retention domain.RetentionPolicy, dryRun bool) []string {
	// Restic defaults to grouping snapshots by host and paths. Database dumps
	// and Docker manifests use a protected random staging directory, so paths
	// legitimately change between runs. The project tag already isolates the
	// selection; grouping by host keeps all runs for that project in one policy
	// group without allowing a changing temporary path to bypass retention.
	args := []string{"forget", "--host", agentID, "--tag", "vaultmesh.project_id=" + projectID, "--group-by", "host"}
	if dryRun {
		args = append(args, "--dry-run", "--json")
	}
	switch retention.Mode {
	case "count":
		args = append(args, "--keep-last", strconv.Itoa(retention.KeepLast))
	case "smart":
		args = append(args,
			"--keep-within-daily", "7d",
			"--keep-within-weekly", "28d",
			"--keep-within-monthly", "1y",
		)
	case "age":
		args = append(args, "--keep-within", retention.KeepWithin)
	default: // gfs and legacy configurations with an empty mode.
		keep := []struct {
			name  string
			value int
		}{
			{"--keep-last", retention.KeepLast}, {"--keep-hourly", retention.KeepHourly},
			{"--keep-daily", retention.KeepDaily}, {"--keep-weekly", retention.KeepWeekly},
			{"--keep-monthly", retention.KeepMonthly}, {"--keep-yearly", retention.KeepYearly},
		}
		for _, item := range keep {
			if item.value > 0 {
				args = append(args, item.name, strconv.Itoa(item.value))
			}
		}
	}
	return args
}

func (r *Runner) ensureRepository(ctx context.Context, repository domain.Repository) *RunResult {
	environment := repositoryEnvironment(repository)
	exitCode, output, err := runCommand(ctx, environment, r.resticPath, resticArguments(repository, "snapshots", "--json")...)
	if err == nil && exitCode == 0 {
		return nil
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &RunResult{Status: domain.RunTimedOut, ErrorCode: "repository_check_timeout", ErrorMessage: "repository check exceeded the configured runtime"}
	}
	if exitCode != 10 {
		message := output
		if message == "" && err != nil {
			message = err.Error()
		}
		return &RunResult{Status: domain.RunFailed, ErrorCode: "repository_unavailable", ErrorMessage: redact(truncate(message, 4096), repository)}
	}
	exitCode, output, err = runCommand(ctx, environment, r.resticPath, resticArguments(repository, "init")...)
	if err != nil || exitCode != 0 {
		if output == "" && err != nil {
			output = err.Error()
		}
		return &RunResult{Status: domain.RunFailed, ErrorCode: "repository_init_failed", ErrorMessage: redact(truncate(output, 4096), repository)}
	}
	return nil
}

type resticSummary struct {
	MessageType         string  `json:"message_type"`
	SnapshotID          string  `json:"snapshot_id"`
	FilesNew            int64   `json:"files_new"`
	FilesChanged        int64   `json:"files_changed"`
	FilesUnmodified     int64   `json:"files_unmodified"`
	TotalFilesProcessed int64   `json:"total_files_processed"`
	TotalBytesProcessed int64   `json:"total_bytes_processed"`
	DataAdded           int64   `json:"data_added"`
	TotalDuration       float64 `json:"total_duration"`
}

func parseResticOutput(reader io.Reader) (resticSummary, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	var summary resticSummary
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var message struct {
			MessageType string `json:"message_type"`
		}
		if err := json.Unmarshal(line, &message); err != nil {
			continue
		}
		if message.MessageType == "summary" {
			if err := json.Unmarshal(line, &summary); err != nil {
				return resticSummary{}, fmt.Errorf("decode restic summary: %w", err)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return resticSummary{}, fmt.Errorf("read restic output: %w", err)
	}
	if summary.MessageType != "summary" {
		return resticSummary{}, errors.New("restic output did not contain a summary message")
	}
	return summary, nil
}

func (r *Runner) prepareSources(ctx context.Context, sources []domain.Source) ([]string, []string, func(), error) {
	var paths, excludes []string
	stagingDirectory := ""
	cleanup := func() {
		if stagingDirectory != "" {
			_ = os.RemoveAll(stagingDirectory)
		}
	}
	ensureStaging := func() (string, error) {
		if stagingDirectory != "" {
			return stagingDirectory, nil
		}
		if r.stagingRoot != "" {
			if err := os.MkdirAll(r.stagingRoot, 0o700); err != nil {
				return "", fmt.Errorf("create staging root: %w", err)
			}
			if err := os.Chmod(r.stagingRoot, 0o700); err != nil {
				return "", fmt.Errorf("secure staging root: %w", err)
			}
		}
		value, err := os.MkdirTemp(r.stagingRoot, "vaultmesh-")
		if err != nil {
			return "", fmt.Errorf("create protected staging directory: %w", err)
		}
		if err := os.Chmod(value, 0o700); err != nil {
			_ = os.RemoveAll(value)
			return "", fmt.Errorf("secure staging directory: %w", err)
		}
		stagingDirectory = value
		return value, nil
	}
	for _, source := range sources {
		switch source.Type {
		case "files":
			for _, path := range source.Paths {
				cleaned := filepath.Clean(path)
				if !filepath.IsAbs(cleaned) {
					cleanup()
					return nil, nil, func() {}, fmt.Errorf("source path %q is not absolute", path)
				}
				if cleaned == "/" || cleaned == "/proc" || strings.HasPrefix(cleaned, "/proc/") ||
					cleaned == "/sys" || strings.HasPrefix(cleaned, "/sys/") ||
					cleaned == "/dev" || strings.HasPrefix(cleaned, "/dev/") {
					cleanup()
					return nil, nil, func() {}, fmt.Errorf("source path %q is blocked by the agent safety policy", cleaned)
				}
				paths = append(paths, cleaned)
			}
			excludes = append(excludes, source.Excludes...)
		case "mysql", "postgresql":
			if source.Database == nil {
				cleanup()
				return nil, nil, func() {}, fmt.Errorf("database source %s has no connection configuration", source.ID)
			}
			directory, err := ensureStaging()
			if err != nil {
				cleanup()
				return nil, nil, func() {}, err
			}
			var output string
			if source.Type == "mysql" {
				output = filepath.Join(directory, source.ID+".mysql.sql")
				err = r.dumpMySQL(ctx, directory, output, *source.Database)
			} else {
				output = filepath.Join(directory, source.ID+".postgres.dump")
				err = r.dumpPostgreSQL(ctx, directory, output, *source.Database)
			}
			if err != nil {
				cleanup()
				return nil, nil, func() {}, fmt.Errorf("prepare %s source %s: %w", source.Type, source.ID, err)
			}
			paths = append(paths, output)
		case "docker":
			if source.Docker == nil {
				cleanup()
				return nil, nil, func() {}, fmt.Errorf("Docker source %s has no container configuration", source.ID)
			}
			directory, err := ensureStaging()
			if err != nil {
				cleanup()
				return nil, nil, func() {}, err
			}
			dockerPaths, manifest, err := r.prepareDockerSource(ctx, *source.Docker)
			if err != nil {
				cleanup()
				return nil, nil, func() {}, fmt.Errorf("prepare Docker source %s: %w", source.ID, err)
			}
			manifestPath := filepath.Join(directory, safeFilename(source.ID)+".docker.json")
			if err := os.WriteFile(manifestPath, manifest, 0o600); err != nil {
				cleanup()
				return nil, nil, func() {}, fmt.Errorf("write Docker source manifest: %w", err)
			}
			paths = append(paths, manifestPath)
			paths = append(paths, dockerPaths...)
		default:
			cleanup()
			return nil, nil, func() {}, fmt.Errorf("source type %q is not supported by this agent version", source.Type)
		}
	}
	if len(paths) == 0 {
		cleanup()
		return nil, nil, func() {}, errors.New("project contains no backup paths or database artifacts")
	}
	return paths, excludes, cleanup, nil
}

type dockerInspection struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Config struct {
		Image string `json:"Image"`
	} `json:"Config"`
	State struct {
		Status string `json:"Status"`
	} `json:"State"`
	Mounts []struct {
		Type        string `json:"Type"`
		Name        string `json:"Name,omitempty"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
		RW          bool   `json:"RW"`
	} `json:"Mounts"`
}

type dockerManifestEntry struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Image  string `json:"image"`
	Status string `json:"status"`
	Mounts any    `json:"mounts"`
}

func (r *Runner) prepareDockerSource(ctx context.Context, source domain.DockerSource) ([]string, []byte, error) {
	seenPaths := make(map[string]struct{})
	var paths []string
	manifest := make([]dockerManifestEntry, 0, len(source.Containers))
	for _, container := range source.Containers {
		command := exec.CommandContext(ctx, r.dockerPath, "inspect", "--type", "container", container)
		var stderr limitedBuffer
		stderr.limit = 4 << 10
		command.Stderr = &stderr
		output, err := command.Output()
		if err != nil {
			message := strings.TrimSpace(stderr.String())
			if message == "" {
				message = err.Error()
			}
			return nil, nil, fmt.Errorf("docker inspect %q failed: %s", container, truncate(message, 4096))
		}
		if len(output) > 4<<20 {
			return nil, nil, fmt.Errorf("docker inspect %q returned more than 4 MiB", container)
		}
		var inspected []dockerInspection
		if err := json.Unmarshal(output, &inspected); err != nil || len(inspected) != 1 {
			return nil, nil, fmt.Errorf("docker inspect %q returned invalid JSON", container)
		}
		item := inspected[0]
		manifest = append(manifest, dockerManifestEntry{
			ID: item.ID, Name: strings.TrimPrefix(item.Name, "/"), Image: item.Config.Image,
			Status: item.State.Status, Mounts: item.Mounts,
		})
		if !source.IncludeVolumes {
			continue
		}
		for _, mount := range item.Mounts {
			if mount.Type != "bind" && mount.Type != "volume" {
				continue
			}
			cleaned, err := safeBackupPath(mount.Source)
			if err != nil {
				return nil, nil, fmt.Errorf("container %q mount %q: %w", container, mount.Destination, err)
			}
			if _, exists := seenPaths[cleaned]; exists {
				continue
			}
			seenPaths[cleaned] = struct{}{}
			paths = append(paths, cleaned)
		}
	}
	encoded, err := json.MarshalIndent(map[string]any{
		"format": "vaultmesh.docker-manifest.v1", "containers": manifest,
	}, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("encode Docker source manifest: %w", err)
	}
	return paths, encoded, nil
}

func safeBackupPath(value string) (string, error) {
	cleaned := filepath.Clean(value)
	if !filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("source path %q is not absolute", value)
	}
	if cleaned == "/" || cleaned == "/proc" || strings.HasPrefix(cleaned, "/proc/") ||
		cleaned == "/sys" || strings.HasPrefix(cleaned, "/sys/") ||
		cleaned == "/dev" || strings.HasPrefix(cleaned, "/dev/") {
		return "", fmt.Errorf("source path %q is blocked by the agent safety policy", cleaned)
	}
	return cleaned, nil
}

func (r *Runner) dumpMySQL(ctx context.Context, directory, output string, database domain.DatabaseSource) error {
	defaultsFile := filepath.Join(directory, "mysql-"+safeFilename(database.Database)+".cnf")
	contents := "[client]\n" +
		"host=\"" + mysqlOptionValue(database.Host) + "\"\n" +
		"port=" + strconv.Itoa(database.Port) + "\n" +
		"user=\"" + mysqlOptionValue(database.Username) + "\"\n" +
		"password=\"" + mysqlOptionValue(database.Password) + "\"\n"
	if err := os.WriteFile(defaultsFile, []byte(contents), 0o600); err != nil {
		return fmt.Errorf("write MySQL client defaults: %w", err)
	}
	args := []string{
		"--defaults-extra-file=" + defaultsFile,
		"--single-transaction",
		"--quick",
		"--routines",
		"--events",
		"--triggers",
		"--databases", database.Database,
		"--result-file=" + output,
	}
	exitCode, commandOutput, err := runCommand(ctx, os.Environ(), r.mysqlDumpPath, args...)
	commandOutput = strings.ReplaceAll(commandOutput, database.Password, "[REDACTED]")
	if err != nil || exitCode != 0 {
		if commandOutput == "" && err != nil {
			commandOutput = err.Error()
		}
		return fmt.Errorf("mysqldump failed: %s", truncate(commandOutput, 4096))
	}
	return requireNonEmptyFile(output)
}

func (r *Runner) dumpPostgreSQL(ctx context.Context, directory, output string, database domain.DatabaseSource) error {
	passFile := filepath.Join(directory, "postgres-"+safeFilename(database.Database)+".pgpass")
	contents := strings.Join([]string{
		pgPassValue(database.Host),
		strconv.Itoa(database.Port),
		pgPassValue(database.Database),
		pgPassValue(database.Username),
		pgPassValue(database.Password),
	}, ":") + "\n"
	if err := os.WriteFile(passFile, []byte(contents), 0o600); err != nil {
		return fmt.Errorf("write PostgreSQL password file: %w", err)
	}
	environment := overrideEnvironment(os.Environ(), map[string]string{"PGPASSFILE": passFile})
	args := []string{
		"--host=" + database.Host,
		"--port=" + strconv.Itoa(database.Port),
		"--username=" + database.Username,
		"--format=custom",
		"--file=" + output,
		database.Database,
	}
	exitCode, commandOutput, err := runCommand(ctx, environment, r.pgDumpPath, args...)
	commandOutput = strings.ReplaceAll(commandOutput, database.Password, "[REDACTED]")
	if err != nil || exitCode != 0 {
		if commandOutput == "" && err != nil {
			commandOutput = err.Error()
		}
		return fmt.Errorf("pg_dump failed: %s", truncate(commandOutput, 4096))
	}
	return requireNonEmptyFile(output)
}

func repositoryEnvironment(repository domain.Repository) []string {
	overrides := make(map[string]string, len(repository.Environment)+2)
	for key, value := range repository.Environment {
		overrides[key] = value
	}
	overrides["RESTIC_REPOSITORY"] = repository.URL
	overrides["RESTIC_PASSWORD"] = repository.Password
	return overrideEnvironment(os.Environ(), overrides)
}

func resticArguments(repository domain.Repository, args ...string) []string {
	keys := make([]string, 0, len(repository.Options))
	for key := range repository.Options {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(args)+len(keys)*2)
	for _, key := range keys {
		result = append(result, "-o", key+"="+repository.Options[key])
	}
	return append(result, args...)
}

func overrideEnvironment(base []string, overrides map[string]string) []string {
	environment := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		key, _, found := strings.Cut(entry, "=")
		if found {
			if _, overridden := overrides[key]; overridden {
				continue
			}
		}
		environment = append(environment, entry)
	}
	for key, value := range overrides {
		environment = append(environment, key+"="+value)
	}
	return environment
}

func runCommand(ctx context.Context, environment []string, executable string, args ...string) (int, string, error) {
	command := exec.CommandContext(ctx, executable, args...)
	command.Env = environment
	var output limitedBuffer
	output.limit = 16 << 10
	command.Stdout = &output
	command.Stderr = &output
	err := command.Run()
	if err == nil {
		return 0, strings.TrimSpace(output.String()), nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode(), strings.TrimSpace(output.String()), err
	}
	return -1, strings.TrimSpace(output.String()), err
}

func runForgetPreview(ctx context.Context, environment []string, executable string, args ...string) (exitCode, kept, removed int, errorOutput string, runErr, parseErr error) {
	command := exec.CommandContext(ctx, executable, args...)
	command.Env = environment
	stdout, err := command.StdoutPipe()
	if err != nil {
		return -1, 0, 0, "", err, nil
	}
	var stderr limitedBuffer
	stderr.limit = 16 << 10
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return -1, 0, 0, stderr.String(), err, nil
	}
	kept, removed, parseErr = countForgetPreview(json.NewDecoder(stdout))
	if parseErr != nil {
		// Keep draining stdout so a Restic process with more output cannot block
		// while Wait is collecting its exit status.
		_, _ = io.Copy(io.Discard, stdout)
	}
	runErr = command.Wait()
	exitCode = 0
	if runErr != nil {
		var exitError *exec.ExitError
		if errors.As(runErr, &exitError) {
			exitCode = exitError.ExitCode()
		} else {
			exitCode = -1
		}
	}
	return exitCode, kept, removed, strings.TrimSpace(stderr.String()), runErr, parseErr
}

func countForgetPreview(decoder *json.Decoder) (int, int, error) {
	var walk func(string) (int, int, error)
	walk = func(field string) (int, int, error) {
		token, err := decoder.Token()
		if err != nil {
			return 0, 0, err
		}
		delimiter, isDelimiter := token.(json.Delim)
		if !isDelimiter {
			return 0, 0, nil
		}
		kept, removed := 0, 0
		switch delimiter {
		case '[':
			for decoder.More() {
				childKept, childRemoved, err := walk("")
				if err != nil {
					return 0, 0, err
				}
				if field == "keep" {
					kept++
				} else if field == "remove" {
					removed++
				} else {
					kept += childKept
					removed += childRemoved
				}
			}
		case '{':
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return 0, 0, err
				}
				childKept, childRemoved, err := walk(key.(string))
				if err != nil {
					return 0, 0, err
				}
				kept += childKept
				removed += childRemoved
			}
		default:
			return 0, 0, fmt.Errorf("unexpected JSON delimiter %q", delimiter)
		}
		if _, err := decoder.Token(); err != nil {
			return 0, 0, err
		}
		return kept, removed, nil
	}
	kept, removed, err := walk("")
	if err != nil {
		return 0, 0, fmt.Errorf("decode Restic retention preview: %w", err)
	}
	return kept, removed, nil
}

func requireNonEmptyFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("database dump was not created: %w", err)
	}
	if info.Size() == 0 {
		return errors.New("database dump is empty")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("secure database dump: %w", err)
	}
	return nil
}

func mysqlOptionValue(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	return strings.ReplaceAll(value, "\"", "\\\"")
}

func pgPassValue(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	return strings.ReplaceAll(value, ":", "\\:")
}

func safeFilename(value string) string {
	digest := []rune(value)
	for index, character := range digest {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_' {
			continue
		}
		digest[index] = '_'
	}
	if len(digest) == 0 {
		return "database"
	}
	return string(digest)
}

func redact(value string, repository domain.Repository) string {
	secrets := []string{repository.Password}
	for _, secret := range repository.Environment {
		secrets = append(secrets, secret)
	}
	for _, secret := range secrets {
		if len(secret) >= 4 {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	return value
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}

type limitedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	original := len(data)
	if b.limit <= 0 {
		return original, nil
	}
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		_, _ = b.buffer.Write(data)
	}
	return original, nil
}

func (b *limitedBuffer) String() string { return b.buffer.String() }
