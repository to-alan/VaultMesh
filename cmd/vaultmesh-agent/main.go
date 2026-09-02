package main

import (
	"context"
	"flag"
	"log/slog"
	"math/rand/v2"
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

const (
	// Loop intervals stay in the same range as the previous fixed tickers, but
	// every cycle adds up to 1/6 of the interval as random jitter so a fleet
	// of agents never fires in lockstep against the control plane.
	syncInterval    = 30 * time.Second
	reportInterval  = 10 * time.Second
	commandInterval = 10 * time.Second
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
	acceptRollback := flag.Bool("accept-rollback", os.Getenv("VAULTMESH_ACCEPT_ROLLBACK") == "true", "allow the next configuration to move to a lower revision after a control-plane restore")
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
	if rejected := state.RejectedReports(); len(rejected) > 0 {
		logger.Warn("agent state contains quarantined run reports",
			"count", len(rejected), "latest_run_id", rejected[0].Report.ID)
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

	if *acceptRollback {
		state.AcceptRollback()
		logger.Warn("configuration rollback accepted once; pass this flag only while recovering a restored control plane")
	}
	// The state directory holds plaintext credentials and the staging/
	// restore directories hold decrypted dumps; none of them may ever be
	// included in a snapshot.
	runner := agent.NewRunnerWithTools(*resticPath, *mysqlDumpPath, *pgDumpPath, *dockerPath, *stagingRoot).
		SetRestoreRoot(*restoreRoot).
		SetProtectedPaths(filepath.Dir(*statePath), *restoreRoot)
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

	// One merged round trip per interval: the heartbeat carries the applied
	// revision and the response delivers a new configuration when the agent
	// lags behind. Each loop adds per-cycle jitter so a fleet of agents never
	// fires in lockstep against the control plane.
	sync := func() { syncAgent(ctx, client, state, manager, identity, logger) }
	reports := func() { flushReports(ctx, client, state, identity, logger) }
	inventories := func() { flushSnapshotInventories(ctx, client, state, identity, logger) }
	commands := func() { fetchCommands(ctx, client, manager, identity, logger) }

	logger.Info("VaultMesh agent started", "agent_id", identity.AgentID, "version", version.Version)
	go loop(ctx, syncInterval, sync)
	go loop(ctx, reportInterval, reports)
	go loop(ctx, reportInterval, inventories)
	go loop(ctx, commandInterval, commands)

	<-ctx.Done()
	manager.Stop()
	logger.Info("VaultMesh agent stopped")
}

// loop runs task immediately and then waits a jittered interval between
// invocations until the context is canceled. The jitter spreads recurring
// requests so agents restarted together do not synchronize into a thundering
// herd against the control plane.
func loop(ctx context.Context, interval time.Duration, task func()) {
	task()
	for {
		delay := jitteredInterval(interval)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		task()
	}
}

func jitteredInterval(interval time.Duration) time.Duration {
	maxJitter := int64(interval / 6)
	return interval + time.Duration(rand.Int64N(maxJitter+1))
}

func syncAgent(ctx context.Context, client *agent.Client, state *agent.StateStore, manager *agent.Manager, identity domain.AgentIdentity, logger *slog.Logger) {
	heartbeat := domain.Heartbeat{AgentInfo: agentInfo(), AppliedRevision: state.Config().Revision}
	config, changed, err := client.Sync(ctx, identity.Token, heartbeat)
	if err != nil {
		logger.Warn("heartbeat and configuration sync", "error", err)
		return
	}
	if !changed {
		return
	}
	for _, degraded := range config.DegradedProjects {
		logger.Warn("control plane omitted a project from this configuration",
			"project_id", degraded.ProjectID, "project_name", degraded.ProjectName, "reason", degraded.Reason)
	}
	if err := manager.Apply(config); err != nil {
		logger.Error("reject invalid configuration", "revision", config.Revision, "error", err)
	}
}

func agentInfo() domain.AgentInfo {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = ""
	}
	return domain.AgentInfo{
		Hostname:     hostname,
		OS:           runtime.GOOS,
		Arch:         runtime.GOARCH,
		AgentVersion: version.Version,
	}
}

func flushSnapshotInventories(ctx context.Context, client *agent.Client, state *agent.StateStore, identity domain.AgentIdentity, logger *slog.Logger) {
	for _, inventory := range state.PendingSnapshotInventories() {
		if err := client.ReportSnapshots(ctx, identity.Token, inventory.ProjectID, inventory.Snapshots); err != nil {
			logger.Warn("report snapshot inventory", "project_id", inventory.ProjectID, "error", err)
			return
		}
		if err := state.AckSnapshotInventory(inventory.ProjectID); err != nil {
			logger.Error("acknowledge snapshot inventory", "project_id", inventory.ProjectID, "error", err)
			return
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

func flushReports(ctx context.Context, client *agent.Client, state *agent.StateStore, identity domain.AgentIdentity, logger *slog.Logger) {
	for _, report := range state.PendingReports() {
		if err := client.Report(ctx, identity.Token, report); err != nil {
			if agent.IsPermanentReportError(err) {
				if quarantineErr := state.QuarantineReport(report.ID, err.Error(), time.Now().UTC()); quarantineErr != nil {
					logger.Error("quarantine rejected run report", "run_id", report.ID, "error", quarantineErr)
					return
				}
				logger.Error("run report permanently rejected and quarantined", "run_id", report.ID, "error", err)
				continue
			}
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
