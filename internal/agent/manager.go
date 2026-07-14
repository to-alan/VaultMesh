package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/to-alan/vaultmesh/internal/domain"
	"github.com/to-alan/vaultmesh/internal/schedule"
)

type Manager struct {
	mu        sync.Mutex
	state     *StateStore
	runner    *Runner
	identity  domain.AgentIdentity
	logger    *slog.Logger
	crons     []*cron.Cron
	repoLocks map[string]chan struct{}
}

func NewManager(state *StateStore, runner *Runner, identity domain.AgentIdentity, logger *slog.Logger) *Manager {
	return &Manager{
		state:     state,
		runner:    runner,
		identity:  identity,
		logger:    logger,
		repoLocks: make(map[string]chan struct{}),
	}
}

func (m *Manager) Apply(config domain.AgentConfig) error {
	prepared := make([]*cron.Cron, 0, len(config.Projects)*2)
	for _, project := range config.Projects {
		backupCron, err := m.operationCron(project, project.Schedule.Cron, project.Schedule.Timezone, "backup")
		if err != nil {
			return err
		}
		prepared = append(prepared, backupCron)
		maintenance := project.Policy.Maintenance
		if !maintenance.Separate {
			continue
		}
		tasks := []struct {
			enabled    bool
			expression string
			operation  string
		}{
			{project.Policy.Retention.Enabled, maintenance.RetentionCron, "retention"},
			{project.Policy.Retention.Enabled && project.Policy.Retention.Prune, maintenance.PruneCron, "prune"},
			{project.Policy.Verification.Mode != "" && project.Policy.Verification.Mode != "off", maintenance.VerificationCron, "verification"},
		}
		for _, task := range tasks {
			if !task.enabled {
				continue
			}
			operationCron, err := m.operationCron(project, task.expression, maintenance.Timezone, task.operation)
			if err != nil {
				return err
			}
			prepared = append(prepared, operationCron)
		}
	}
	if err := m.state.SetConfig(config); err != nil {
		return err
	}

	m.mu.Lock()
	old := m.crons
	m.crons = append([]*cron.Cron(nil), prepared...)
	m.mu.Unlock()
	for _, runner := range old {
		runner.Stop()
	}
	for _, runner := range prepared {
		runner.Start()
	}
	m.logger.Info("applied agent configuration", "revision", config.Revision, "projects", len(config.Projects))
	return nil
}

func (m *Manager) operationCron(project domain.AgentProject, expression, timezone, operation string) (*cron.Cron, error) {
	if err := schedule.Validate(expression, timezone); err != nil {
		return nil, fmt.Errorf("project %s %s schedule: %w", project.ID, operation, err)
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, err
	}
	projectCopy := project
	cronRunner := cron.New(cron.WithLocation(location), cron.WithParser(schedule.Parser))
	if _, err := cronRunner.AddFunc(expression, func() {
		scheduledAt := time.Now().UTC().Truncate(time.Minute)
		go m.executeOperation(projectCopy, scheduledAt, operation)
	}); err != nil {
		return nil, fmt.Errorf("register project %s %s schedule: %w", project.ID, operation, err)
	}
	return cronRunner, nil
}

func (m *Manager) Stop() {
	m.mu.Lock()
	crons := m.crons
	m.crons = nil
	m.mu.Unlock()
	for _, runner := range crons {
		runner.Stop()
	}
}

func (m *Manager) Manual(command domain.Command) error {
	config := m.state.Config()
	for _, project := range config.Projects {
		if project.ID != command.ProjectID {
			continue
		}
		scheduledAt := time.Now().UTC()
		switch command.Type {
		case "backup", "retention_preview", "snapshot_sync", "snapshot_protect", "snapshot_browse", "snapshot_restore":
		default:
			return fmt.Errorf("unsupported command type %q", command.Type)
		}
		go m.executeWithKey(project, scheduledAt, "manual:"+command.ID, command.Type, command.ID, command.Payload)
		return nil
	}
	return fmt.Errorf("project %s is not present in applied configuration revision %d", command.ProjectID, config.Revision)
}

func (m *Manager) executeOperation(project domain.AgentProject, scheduledAt time.Time, operation string) {
	m.executeWithKey(project, scheduledAt, project.ID+":"+operation+":"+scheduledAt.UTC().Format(time.RFC3339), operation, "", nil)
}

func (m *Manager) executeWithKey(project domain.AgentProject, scheduledAt time.Time, idempotencyKey, operation, commandID string, payload map[string]any) {
	runID := deterministicRunID(idempotencyKey)
	now := time.Now().UTC()
	report := domain.RunReport{
		ID:             runID,
		IdempotencyKey: idempotencyKey,
		ProjectID:      project.ID,
		ScheduledAt:    scheduledAt,
		StartedAt:      now,
		Status:         domain.RunRunning,
		Stats:          map[string]any{"operation": operation},
	}
	claimed, err := m.state.BeginRun(report)
	if err != nil {
		m.logger.Error("persist scheduled run", "project_id", project.ID, "error", err)
		return
	}
	if !claimed {
		m.logger.Debug("skipping duplicate scheduled run", "project_id", project.ID, "scheduled_at", scheduledAt)
		return
	}

	maxRuntime := time.Duration(project.Schedule.MaxRuntimeSeconds) * time.Second
	if maxRuntime <= 0 {
		maxRuntime = 6 * time.Hour
	}
	ctx, cancel := context.WithTimeout(context.Background(), maxRuntime)
	defer cancel()
	if jitter := project.Schedule.JitterSeconds; jitter > 0 {
		delay := time.Duration(rand.IntN(jitter+1)) * time.Second
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			m.finishTimedOut(report, "jitter exceeded the configured runtime")
			return
		case <-timer.C:
		}
	}

	release, err := m.acquireRepository(ctx, project.Repository.ID)
	if err != nil {
		m.finishTimedOut(report, "timed out waiting for another operation on the same repository")
		return
	}
	defer release()
	var result RunResult
	switch operation {
	case "retention_preview":
		result = m.runner.PreviewRetention(ctx, m.identity.AgentID, project)
	case "retention":
		result = m.runner.ApplyRetention(ctx, m.identity.AgentID, project)
	case "prune":
		result = m.runner.Prune(ctx, project)
	case "verification":
		result = m.runner.Verify(ctx, project)
	case "snapshot_sync":
		result = m.runner.ListSnapshots(ctx, m.identity.AgentID, project)
	case "snapshot_protect":
		result = m.runner.ProtectSnapshot(ctx, m.identity.AgentID, project, payloadString(payload, "snapshot_id"), payloadBool(payload, "protected"))
	case "snapshot_browse":
		result = m.runner.BrowseSnapshot(ctx, project, payloadString(payload, "snapshot_id"), payloadString(payload, "path"))
	case "snapshot_restore":
		result = m.runner.RestoreSnapshot(ctx, commandID, project, payloadString(payload, "snapshot_id"), payloadString(payload, "path"))
	default:
		result = m.runner.Execute(ctx, m.identity.AgentID, project)
	}
	if operation == "backup" && result.SnapshotID != "" {
		inventory := m.runner.ListSnapshots(ctx, m.identity.AgentID, project)
		if result.Stats == nil {
			result.Stats = make(map[string]any)
		}
		if inventory.Status == domain.RunSucceeded {
			result.Stats["snapshots"] = inventory.Stats["snapshots"]
			result.Stats["snapshot_count"] = inventory.Stats["snapshot_count"]
		} else {
			result.Stats["snapshot_sync_error"] = inventory.ErrorMessage
		}
	}
	finished := time.Now().UTC()
	report.FinishedAt = &finished
	report.Status = result.Status
	report.SnapshotID = result.SnapshotID
	report.ErrorCode = result.ErrorCode
	report.ErrorMessage = result.ErrorMessage
	report.Stats = result.Stats
	if err := m.state.FinishRun(report); err != nil {
		m.logger.Error("persist run result", "run_id", report.ID, "error", err)
		return
	}
	m.logger.Info("agent operation finished", "operation", operation, "run_id", report.ID, "project_id", project.ID, "status", report.Status, "snapshot_id", report.SnapshotID)
}

func payloadString(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return value
}

func payloadBool(payload map[string]any, key string) bool {
	value, _ := payload[key].(bool)
	return value
}

func (m *Manager) acquireRepository(ctx context.Context, repositoryID string) (func(), error) {
	m.mu.Lock()
	semaphore, ok := m.repoLocks[repositoryID]
	if !ok {
		semaphore = make(chan struct{}, 1)
		semaphore <- struct{}{}
		m.repoLocks[repositoryID] = semaphore
	}
	m.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-semaphore:
		return func() { semaphore <- struct{}{} }, nil
	}
}

func (m *Manager) finishTimedOut(report domain.RunReport, message string) {
	finished := time.Now().UTC()
	report.FinishedAt = &finished
	report.Status = domain.RunTimedOut
	report.ErrorCode = "max_runtime_exceeded"
	report.ErrorMessage = message
	if err := m.state.FinishRun(report); err != nil {
		m.logger.Error("persist timed out run", "run_id", report.ID, "error", err)
	}
}

func deterministicRunID(idempotencyKey string) string {
	digest := sha256.Sum256([]byte(idempotencyKey))
	return "run_" + hex.EncodeToString(digest[:12])
}
