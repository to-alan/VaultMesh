package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/to-alan/vaultmesh/internal/domain"
)

const protectedSnapshotTag = "vaultmesh.protected=true"

var (
	resticSnapshotID = regexp.MustCompile(`^[a-f0-9]{64}$`)
	commandIDPattern = regexp.MustCompile(`^cmd_[A-Za-z0-9_-]{8,64}$`)
)

type resticSnapshot struct {
	ID       string    `json:"id"`
	Time     time.Time `json:"time"`
	Hostname string    `json:"hostname"`
	Username string    `json:"username"`
	Paths    []string  `json:"paths"`
	Tags     []string  `json:"tags"`
	Summary  struct {
		TotalFilesProcessed int64 `json:"total_files_processed"`
		TotalBytesProcessed int64 `json:"total_bytes_processed"`
	} `json:"summary"`
}

func (r *Runner) ListSnapshots(ctx context.Context, agentID string, project domain.AgentProject) RunResult {
	stats := map[string]any{"operation": "snapshot_sync"}
	var raw []resticSnapshot
	args := resticArguments(project.Repository,
		"snapshots", "--json", "--host", agentID, "--tag", "vaultmesh.project_id="+project.ID)
	exitCode, errorOutput, runErr, decodeErr := runJSONDocument(ctx, repositoryEnvironment(project.Repository), r.resticPath, args, &raw)
	stats["restic_exit_code"] = exitCode
	if result := commandFailure(ctx, project.Repository, exitCode, errorOutput, runErr, decodeErr, "snapshot_sync_failed", "snapshot inventory"); result != nil {
		result.Stats = stats
		return *result
	}
	snapshots := make([]domain.Snapshot, 0, len(raw))
	for _, item := range raw {
		if !resticSnapshotID.MatchString(item.ID) {
			continue
		}
		snapshot := domain.Snapshot{
			ID: item.ID, ProjectID: project.ID, ServerID: agentID, Time: item.Time,
			Hostname: item.Hostname, Username: item.Username,
			Paths: append([]string(nil), item.Paths...), Tags: append([]string(nil), item.Tags...),
			TotalFiles: item.Summary.TotalFilesProcessed, TotalBytes: item.Summary.TotalBytesProcessed,
		}
		for _, tag := range item.Tags {
			if tag == protectedSnapshotTag {
				snapshot.Protected = true
				break
			}
		}
		snapshots = append(snapshots, snapshot)
	}
	stats["snapshots"] = snapshots
	stats["snapshot_count"] = len(snapshots)
	return RunResult{Status: domain.RunSucceeded, Stats: stats}
}

func (r *Runner) ProtectSnapshot(ctx context.Context, agentID string, project domain.AgentProject, snapshotID string, protected bool) RunResult {
	stats := map[string]any{"operation": "snapshot_protect", "snapshot_id": snapshotID, "protected": protected}
	if !resticSnapshotID.MatchString(snapshotID) {
		return RunResult{Status: domain.RunFailed, ErrorCode: "invalid_snapshot_id", ErrorMessage: "snapshot ID must be a full Restic ID", Stats: stats}
	}
	action := "--add"
	if !protected {
		action = "--remove"
	}
	exitCode, output, err := runCommand(ctx, repositoryEnvironment(project.Repository), r.resticPath,
		resticArguments(project.Repository, "tag", action, protectedSnapshotTag, snapshotID)...)
	stats["restic_exit_code"] = exitCode
	if result := commandFailure(ctx, project.Repository, exitCode, output, err, nil, "snapshot_protect_failed", "snapshot protection"); result != nil {
		result.Stats = stats
		return *result
	}
	inventory := r.ListSnapshots(ctx, agentID, project)
	if inventory.Status != domain.RunSucceeded {
		inventory.ErrorCode = "snapshot_resync_failed"
		inventory.Stats["operation"] = "snapshot_protect"
		return inventory
	}
	stats["snapshots"] = inventory.Stats["snapshots"]
	stats["snapshot_count"] = inventory.Stats["snapshot_count"]
	return RunResult{Status: domain.RunSucceeded, Stats: stats}
}

func (r *Runner) BrowseSnapshot(ctx context.Context, project domain.AgentProject, snapshotID, snapshotPath string) RunResult {
	stats := map[string]any{"operation": "snapshot_browse", "snapshot_id": snapshotID, "path": snapshotPath}
	cleaned, err := validateRepositoryPath(snapshotID, snapshotPath)
	if err != nil {
		return RunResult{Status: domain.RunFailed, ErrorCode: "invalid_snapshot_path", ErrorMessage: err.Error(), Stats: stats}
	}
	command := exec.CommandContext(ctx, r.resticPath, resticArguments(project.Repository, "ls", "--json", snapshotID, cleaned)...)
	command.Env = repositoryEnvironment(project.Repository)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return RunResult{Status: domain.RunFailed, ErrorCode: "snapshot_browse_failed", ErrorMessage: err.Error(), Stats: stats}
	}
	var stderr limitedBuffer
	stderr.limit = 16 << 10
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return RunResult{Status: domain.RunFailed, ErrorCode: "snapshot_browse_failed", ErrorMessage: redact(err.Error(), project.Repository), Stats: stats}
	}
	entries, parseErr := parseSnapshotEntries(stdout, cleaned, 5000)
	if parseErr != nil {
		_, _ = io.Copy(io.Discard, stdout)
	}
	runErr := command.Wait()
	exitCode := processExitCode(runErr)
	stats["restic_exit_code"] = exitCode
	if result := commandFailure(ctx, project.Repository, exitCode, stderr.String(), runErr, parseErr, "snapshot_browse_failed", "snapshot browse"); result != nil {
		result.Stats = stats
		return *result
	}
	stats["entries"] = entries
	stats["entry_count"] = len(entries)
	return RunResult{Status: domain.RunSucceeded, Stats: stats}
}

func (r *Runner) RestoreSnapshot(ctx context.Context, commandID string, project domain.AgentProject, snapshotID, snapshotPath string) RunResult {
	stats := map[string]any{"operation": "snapshot_restore", "snapshot_id": snapshotID, "path": snapshotPath}
	cleaned, err := validateRepositoryPath(snapshotID, snapshotPath)
	if err != nil {
		return RunResult{Status: domain.RunFailed, ErrorCode: "invalid_snapshot_path", ErrorMessage: err.Error(), Stats: stats}
	}
	if r.restoreRoot == "" {
		return RunResult{Status: domain.RunFailed, ErrorCode: "restore_root_missing", ErrorMessage: "Agent restore root is not configured", Stats: stats}
	}
	if !commandIDPattern.MatchString(commandID) {
		return RunResult{Status: domain.RunFailed, ErrorCode: "invalid_command_id", ErrorMessage: "restore command ID is invalid", Stats: stats}
	}
	root := filepath.Clean(r.restoreRoot)
	if !filepath.IsAbs(root) {
		return RunResult{Status: domain.RunFailed, ErrorCode: "restore_root_invalid", ErrorMessage: "Agent restore root must be absolute", Stats: stats}
	}
	if filepath.Dir(root) == root {
		return RunResult{Status: domain.RunFailed, ErrorCode: "restore_root_invalid", ErrorMessage: "Agent restore root cannot be a filesystem root", Stats: stats}
	}
	if rootInfo, err := os.Lstat(root); err == nil {
		if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
			return RunResult{Status: domain.RunFailed, ErrorCode: "restore_root_invalid", ErrorMessage: "Agent restore root must be a real directory, not a symbolic link", Stats: stats}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return RunResult{Status: domain.RunFailed, ErrorCode: "restore_target_failed", ErrorMessage: err.Error(), Stats: stats}
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return RunResult{Status: domain.RunFailed, ErrorCode: "restore_target_failed", ErrorMessage: err.Error(), Stats: stats}
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return RunResult{Status: domain.RunFailed, ErrorCode: "restore_root_invalid", ErrorMessage: "Agent restore root must be a real directory, not a symbolic link", Stats: stats}
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return RunResult{Status: domain.RunFailed, ErrorCode: "restore_target_failed", ErrorMessage: err.Error(), Stats: stats}
	}
	target := filepath.Join(root, commandID)
	if _, err := os.Lstat(target); err == nil || !errors.Is(err, os.ErrNotExist) {
		return RunResult{Status: domain.RunFailed, ErrorCode: "restore_target_exists", ErrorMessage: "restore target already exists; VaultMesh will not overwrite it", Stats: stats}
	}
	args := resticArguments(project.Repository,
		"restore", "--json", "--target", target, "--overwrite", "never", snapshotID+":"+cleaned)
	summary, exitCode, errorOutput, runErr, parseErr := runRestore(ctx, repositoryEnvironment(project.Repository), r.resticPath, args)
	stats["restic_exit_code"] = exitCode
	stats["restore_target"] = target
	if result := commandFailure(ctx, project.Repository, exitCode, errorOutput, runErr, parseErr, "snapshot_restore_failed", "snapshot restore"); result != nil {
		result.Stats = stats
		return *result
	}
	stats["total_files"] = summary.TotalFiles
	stats["files_restored"] = summary.FilesRestored
	stats["files_skipped"] = summary.FilesSkipped
	stats["total_bytes"] = summary.TotalBytes
	stats["bytes_restored"] = summary.BytesRestored
	return RunResult{Status: domain.RunSucceeded, Stats: stats}
}

func validateRepositoryPath(snapshotID, value string) (string, error) {
	if !resticSnapshotID.MatchString(snapshotID) {
		return "", errors.New("snapshot ID must be a full Restic ID")
	}
	if strings.ContainsAny(value, "\x00\r\n") || !strings.HasPrefix(value, "/") {
		return "", errors.New("snapshot path must be absolute")
	}
	cleaned := path.Clean(value)
	if !strings.HasPrefix(cleaned, "/") {
		return "", errors.New("snapshot path must be absolute")
	}
	return cleaned, nil
}

func runJSONDocument(ctx context.Context, environment []string, executable string, args []string, output any) (int, string, error, error) {
	command := exec.CommandContext(ctx, executable, args...)
	command.Env = environment
	stdout, err := command.StdoutPipe()
	if err != nil {
		return -1, "", err, nil
	}
	var stderr limitedBuffer
	stderr.limit = 16 << 10
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return -1, stderr.String(), err, nil
	}
	decodeErr := json.NewDecoder(stdout).Decode(output)
	_, _ = io.Copy(io.Discard, stdout)
	runErr := command.Wait()
	return processExitCode(runErr), strings.TrimSpace(stderr.String()), runErr, decodeErr
}

func parseSnapshotEntries(reader io.Reader, currentPath string, limit int) ([]domain.SnapshotEntry, error) {
	const maxSerializedEntryBytes = 512 << 10
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	entries := make([]domain.SnapshotEntry, 0)
	tooMany := false
	serializedBytes := 0
	for scanner.Scan() {
		var message struct {
			MessageType string    `json:"message_type"`
			StructType  string    `json:"struct_type"`
			Name        string    `json:"name"`
			Path        string    `json:"path"`
			Type        string    `json:"type"`
			Size        int64     `json:"size"`
			Mode        uint32    `json:"mode"`
			Permissions string    `json:"permissions"`
			MTime       time.Time `json:"mtime"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			return nil, fmt.Errorf("decode Restic ls output: %w", err)
		}
		if message.MessageType != "node" && message.StructType != "node" {
			continue
		}
		if path.Clean(message.Path) == currentPath {
			continue
		}
		if len(entries) >= limit {
			tooMany = true
			continue
		}
		entryBytes := len(message.Name) + len(message.Path) + len(message.Type) + len(message.Permissions) + 128
		if serializedBytes+entryBytes > maxSerializedEntryBytes {
			tooMany = true
			continue
		}
		entries = append(entries, domain.SnapshotEntry{
			Name: message.Name, Path: message.Path, Type: message.Type, Size: message.Size,
			Mode: message.Mode, Permissions: message.Permissions, ModifiedAt: message.MTime,
		})
		serializedBytes += entryBytes
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if tooMany {
		return nil, fmt.Errorf("snapshot directory exceeds the safe response limit (%d entries or %d KiB)", limit, maxSerializedEntryBytes>>10)
	}
	return entries, nil
}

type restoreSummary struct {
	MessageType   string `json:"message_type"`
	TotalFiles    int64  `json:"total_files"`
	FilesRestored int64  `json:"files_restored"`
	FilesSkipped  int64  `json:"files_skipped"`
	TotalBytes    int64  `json:"total_bytes"`
	BytesRestored int64  `json:"bytes_restored"`
}

func runRestore(ctx context.Context, environment []string, executable string, args []string) (restoreSummary, int, string, error, error) {
	command := exec.CommandContext(ctx, executable, args...)
	command.Env = environment
	stdout, err := command.StdoutPipe()
	if err != nil {
		return restoreSummary{}, -1, "", err, nil
	}
	var stderr limitedBuffer
	stderr.limit = 16 << 10
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return restoreSummary{}, -1, stderr.String(), err, nil
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	var summary restoreSummary
	var parseErr error
	for scanner.Scan() {
		var message struct {
			MessageType string `json:"message_type"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			parseErr = err
			continue
		}
		if message.MessageType == "summary" {
			parseErr = json.Unmarshal(scanner.Bytes(), &summary)
		}
	}
	if err := scanner.Err(); err != nil {
		parseErr = err
	}
	runErr := command.Wait()
	if parseErr == nil && summary.MessageType != "summary" {
		parseErr = errors.New("Restic restore output did not contain a summary")
	}
	return summary, processExitCode(runErr), strings.TrimSpace(stderr.String()), runErr, parseErr
}

func commandFailure(ctx context.Context, repository domain.Repository, exitCode int, errorOutput string, runErr, parseErr error, errorCode, operation string) *RunResult {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &RunResult{Status: domain.RunTimedOut, ErrorCode: "max_runtime_exceeded", ErrorMessage: operation + " exceeded the configured maximum runtime"}
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return &RunResult{Status: domain.RunCanceled, ErrorCode: "canceled", ErrorMessage: operation + " was canceled"}
	}
	if runErr != nil || exitCode != 0 {
		if errorOutput == "" && runErr != nil {
			errorOutput = runErr.Error()
		}
		return &RunResult{Status: domain.RunFailed, ErrorCode: errorCode, ErrorMessage: redact(truncate(errorOutput, 4096), repository)}
	}
	if parseErr != nil {
		return &RunResult{Status: domain.RunFailed, ErrorCode: "invalid_restic_output", ErrorMessage: parseErr.Error()}
	}
	return nil
}

func processExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode()
	}
	return -1
}
