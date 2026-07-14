package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/to-alan/vaultmesh/internal/agent"
	"github.com/to-alan/vaultmesh/internal/domain"
	"github.com/to-alan/vaultmesh/internal/version"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	defaultState := os.Getenv("VAULTMESH_AGENT_STATE")
	if defaultState == "" {
		defaultState = defaultStatePath()
	}
	serverURL := flag.String("server", os.Getenv("VAULTMESH_SERVER_URL"), "VaultMesh control plane URL")
	enrollmentToken := flag.String("enrollment-token", os.Getenv("VAULTMESH_ENROLLMENT_TOKEN"), "one-time enrollment token")
	statePath := flag.String("state", defaultState, "path to the persistent agent state")
	resticPath := flag.String("restic", envOr("VAULTMESH_RESTIC_PATH", "restic"), "path to the restic executable")
	mysqlDumpPath := flag.String("mysqldump", envOr("VAULTMESH_MYSQLDUMP_PATH", "mysqldump"), "path to the mysqldump executable")
	pgDumpPath := flag.String("pg-dump", envOr("VAULTMESH_PG_DUMP_PATH", "pg_dump"), "path to the pg_dump executable")
	dockerPath := flag.String("docker", envOr("VAULTMESH_DOCKER_PATH", "docker"), "path to the Docker CLI executable")
	stagingRoot := flag.String("staging-root", os.Getenv("VAULTMESH_STAGING_ROOT"), "parent directory for protected temporary database dumps")
	restoreRoot := flag.String("restore-root", envOr("VAULTMESH_RESTORE_ROOT", defaultRestoreRoot()), "directory for isolated restore jobs")
	flag.Parse()
	if strings.TrimSpace(*serverURL) == "" {
		logger.Error("control plane URL is required", "flag", "--server")
		os.Exit(2)
	}
	client, err := agent.NewClient(*serverURL, version.Version)
	if err != nil {
		logger.Error("invalid control plane URL", "error", err)
		os.Exit(2)
	}
	state, err := agent.OpenState(*statePath)
	if err != nil {
		logger.Error("open agent state", "error", err)
		os.Exit(1)
	}
	hostname, err := os.Hostname()
	if err != nil {
		logger.Error("read hostname", "error", err)
		os.Exit(1)
	}
	info := domain.AgentInfo{
		Hostname:     hostname,
		OS:           runtime.GOOS,
		Arch:         runtime.GOARCH,
		AgentVersion: version.Version,
	}
	identity, enrolled := state.Identity()
	if !enrolled {
		if strings.TrimSpace(*enrollmentToken) == "" {
			logger.Error("agent is not enrolled; one-time enrollment token is required", "flag", "--enrollment-token")
			os.Exit(2)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		identity, err = client.Enroll(ctx, *enrollmentToken, info)
		cancel()
		if err != nil {
			logger.Error("enroll agent", "error", err)
			os.Exit(1)
		}
		if err := state.SetIdentity(identity); err != nil {
			logger.Error("persist agent identity", "error", err)
			os.Exit(1)
		}
		logger.Info("agent enrolled", "agent_id", identity.AgentID)
	}

	runner := agent.NewRunnerWithTools(*resticPath, *mysqlDumpPath, *pgDumpPath, *dockerPath, *stagingRoot).SetRestoreRoot(*restoreRoot)
	manager := agent.NewManager(state, runner, identity, logger)
	if cached := state.Config(); cached.Revision > 0 || len(cached.Projects) > 0 {
		if err := manager.Apply(cached); err != nil {
			logger.Error("apply cached configuration", "revision", cached.Revision, "error", err)
			os.Exit(1)
		}
	}
	defer manager.Stop()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	syncConfig(ctx, client, state, manager, identity, logger)
	sendHeartbeat(ctx, client, state, identity, info, logger)
	flushReports(ctx, client, state, identity, logger)
	fetchCommands(ctx, client, manager, identity, logger)

	configTicker := time.NewTicker(30 * time.Second)
	heartbeatTicker := time.NewTicker(30 * time.Second)
	reportTicker := time.NewTicker(10 * time.Second)
	commandTicker := time.NewTicker(10 * time.Second)
	defer configTicker.Stop()
	defer heartbeatTicker.Stop()
	defer reportTicker.Stop()
	defer commandTicker.Stop()
	logger.Info("VaultMesh agent started", "agent_id", identity.AgentID, "version", version.Version)
	for {
		select {
		case <-ctx.Done():
			logger.Info("VaultMesh agent stopped")
			return
		case <-configTicker.C:
			syncConfig(ctx, client, state, manager, identity, logger)
		case <-heartbeatTicker.C:
			sendHeartbeat(ctx, client, state, identity, info, logger)
		case <-reportTicker.C:
			flushReports(ctx, client, state, identity, logger)
		case <-commandTicker.C:
			fetchCommands(ctx, client, manager, identity, logger)
		}
	}
}

func fetchCommands(ctx context.Context, client *agent.Client, manager *agent.Manager, identity domain.AgentIdentity, logger *slog.Logger) {
	commands, err := client.Commands(ctx, identity.Token)
	if err != nil {
		logger.Warn("fetch manual commands", "error", err)
		return
	}
	for _, command := range commands {
		switch command.Type {
		case "backup", "retention_preview", "snapshot_sync", "snapshot_protect", "snapshot_browse", "snapshot_restore":
		default:
			logger.Error("reject unsupported command", "command_id", command.ID, "type", command.Type)
			continue
		}
		if err := manager.Manual(command); err != nil {
			logger.Warn("defer manual command", "command_id", command.ID, "type", command.Type, "error", err)
		}
	}
}

func syncConfig(ctx context.Context, client *agent.Client, state *agent.StateStore, manager *agent.Manager, identity domain.AgentIdentity, logger *slog.Logger) {
	revision := state.Config().Revision
	config, err := client.Config(ctx, identity.Token, revision)
	if errors.Is(err, agent.ErrNotModified) {
		return
	}
	if err != nil {
		logger.Warn("synchronize configuration", "error", err)
		return
	}
	if err := manager.Apply(config); err != nil {
		logger.Error("reject invalid configuration", "revision", config.Revision, "error", err)
	}
}

func sendHeartbeat(ctx context.Context, client *agent.Client, state *agent.StateStore, identity domain.AgentIdentity, info domain.AgentInfo, logger *slog.Logger) {
	heartbeat := domain.Heartbeat{AgentInfo: info, AppliedRevision: state.Config().Revision}
	if err := client.Heartbeat(ctx, identity.Token, heartbeat); err != nil {
		logger.Warn("send heartbeat", "error", err)
	}
}

func flushReports(ctx context.Context, client *agent.Client, state *agent.StateStore, identity domain.AgentIdentity, logger *slog.Logger) {
	for _, report := range state.PendingReports() {
		if err := client.Report(ctx, identity.Token, report); err != nil {
			logger.Warn("report backup run", "run_id", report.ID, "error", err)
			return
		}
		if err := state.AckReport(report.ID); err != nil {
			logger.Error("acknowledge reported run", "run_id", report.ID, "error", err)
			return
		}
	}
}

func defaultStatePath() string {
	if runtime.GOOS == "linux" {
		return "/var/lib/vaultmesh-agent/state.json"
	}
	directory, err := os.UserConfigDir()
	if err != nil {
		return "vaultmesh-agent-state.json"
	}
	return filepath.Join(directory, "vaultmesh-agent", "state.json")
}

func defaultRestoreRoot() string {
	if runtime.GOOS == "linux" {
		return "/var/lib/vaultmesh-agent/restores"
	}
	directory, err := os.UserConfigDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "vaultmesh-agent-restores")
	}
	return filepath.Join(directory, "vaultmesh-agent", "restores")
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
