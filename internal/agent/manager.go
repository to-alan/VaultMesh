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
	type preparedCron struct {
		cron    *cron.Cron
		project domain.AgentProject
	}
	prepared := make([]preparedCron, 0, len(config.Projects))
	for _, project := range config.Projects {
		if err := schedule.Validate(project.Schedule.Cron, project.Schedule.Timezone); err != nil {
			return fmt.Errorf("project %s schedule: %w", project.ID, err)
		}
		location, err := time.LoadLocation(project.Schedule.Timezone)
		if err != nil {
			return err
		}
		projectCopy := project
		cronRunner := cron.New(cron.WithLocation(location), cron.WithParser(schedule.Parser))
		if _, err := cronRunner.AddFunc(project.Schedule.Cron, func() {
			scheduledAt := time.Now().UTC().Truncate(time.Minute)
			go m.execute(projectCopy, scheduledAt)
		}); err != nil {
			return fmt.Errorf("register project %s schedule: %w", project.ID, err)
		}
		prepared = append(prepared, preparedCron{cron: cronRunner, project: project})
	}
	if err := m.state.SetConfig(config); err != nil {
		return err
	}

	m.mu.Lock()
	old := m.crons
	m.crons = make([]*cron.Cron, 0, len(prepared))
	for _, item := range prepared {
		m.crons = append(m.crons, item.cron)
	}
	m.mu.Unlock()
	for _, runner := range old {
		runner.Stop()
	}
	for _, item := range prepared {
		item.cron.Start()
	}
	m.logger.Info("applied agent configuration", "revision", config.Revision, "projects", len(config.Projects))
	return nil
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

func (m *Manager) Manual(projectID, commandID string) error {
	config := m.state.Config()
	for _, project := range config.Projects {
		if project.ID != projectID {
			continue
		}
		scheduledAt := time.Now().UTC()
		go m.executeWithKey(project, scheduledAt, "manual:"+commandID)
		return nil
	}
	return fmt.Errorf("project %s is not present in applied configuration revision %d", projectID, config.Revision)
}

func (m *Manager) execute(project domain.AgentProject, scheduledAt time.Time) {
	m.executeWithKey(project, scheduledAt, project.ID+":"+scheduledAt.UTC().Format(time.RFC3339))
}

func (m *Manager) executeWithKey(project domain.AgentProject, scheduledAt time.Time, idempotencyKey string) {
	runID := deterministicRunID(idempotencyKey)
	now := time.Now().UTC()
	report := domain.RunReport{
		ID:             runID,
		IdempotencyKey: idempotencyKey,
		ProjectID:      project.ID,
		ScheduledAt:    scheduledAt,
		StartedAt:      now,
		Status:         domain.RunRunning,
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
	result := m.runner.Execute(ctx, m.identity.AgentID, project)
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
	m.logger.Info("backup run finished", "run_id", report.ID, "project_id", project.ID, "status", report.Status, "snapshot_id", report.SnapshotID)
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
