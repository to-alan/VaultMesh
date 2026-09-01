package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/to-alan/vaultmesh/internal/domain"
)

const (
	maxRejectedReports    = 200
	maxReportRejectReason = 4096
)

// RejectedReport is a run report that the Control Plane permanently rejected.
// It remains in the root-only Agent state for diagnosis, but no longer blocks
// newer reports in the ordered Outbox.
type RejectedReport struct {
	Report     domain.RunReport `json:"report"`
	Reason     string           `json:"reason"`
	RejectedAt time.Time        `json:"rejected_at"`
}

type persistedState struct {
	Identity        *domain.AgentIdentity       `json:"identity,omitempty"`
	Config          domain.AgentConfig          `json:"config"`
	RunKeys         map[string]string           `json:"run_keys"`
	Runs            map[string]domain.RunReport `json:"runs"`
	Outbox          map[string]domain.RunReport `json:"outbox"`
	RejectedReports map[string]RejectedReport   `json:"rejected_reports,omitempty"`
	// SnapshotInventories holds the latest verified inventory per project until
	// the dedicated control-plane endpoint acknowledges delivery. Only the most
	// recent inventory per project is kept: older entries can never converge a
	// control-plane index that a newer one cannot fully replace.
	SnapshotInventories map[string]SnapshotInventory `json:"snapshot_inventories,omitempty"`
}

// SnapshotInventory is a pending delivery of a project's Restic snapshot index.
type SnapshotInventory struct {
	Snapshots []domain.Snapshot `json:"snapshots"`
	QueuedAt  time.Time         `json:"queued_at"`
}

type StateStore struct {
	mu            sync.Mutex
	path          string
	state         persistedState
	allowRollback bool
}

func OpenState(path string) (*StateStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("agent state path is required")
	}
	directory := filepath.Dir(path)
	if directory != "." {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, fmt.Errorf("create state directory: %w", err)
		}
		if err := os.Chmod(directory, 0o700); err != nil {
			return nil, fmt.Errorf("secure state directory: %w", err)
		}
	}
	store := &StateStore{path: path}
	store.state.RunKeys = make(map[string]string)
	store.state.Runs = make(map[string]domain.RunReport)
	store.state.Outbox = make(map[string]domain.RunReport)
	store.state.RejectedReports = make(map[string]RejectedReport)
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read agent state: %w", err)
	}
	if err == nil {
		if err := json.Unmarshal(data, &store.state); err != nil {
			return nil, fmt.Errorf("decode agent state: %w", err)
		}
		store.initializeMaps()
		if err := os.Chmod(path, 0o600); err != nil {
			return nil, fmt.Errorf("secure agent state: %w", err)
		}
	}
	if store.recoverInterruptedRuns() {
		if err := store.saveLocked(); err != nil {
			return nil, err
		}
	}
	return store, nil
}

func (s *StateStore) Identity() (domain.AgentIdentity, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Identity == nil {
		return domain.AgentIdentity{}, false
	}
	return *s.state.Identity, true
}

func (s *StateStore) SetIdentity(identity domain.AgentIdentity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Identity != nil && s.state.Identity.AgentID != identity.AgentID {
		return errors.New("agent is already enrolled to another identity")
	}
	previous := s.state.Identity
	copy := identity
	s.state.Identity = &copy
	if err := s.saveLocked(); err != nil {
		s.state.Identity = previous
		return err
	}
	return nil
}

func (s *StateStore) Config() domain.AgentConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneAgentConfig(s.state.Config)
}

func (s *StateStore) SetConfig(config domain.AgentConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if config.Revision < s.state.Config.Revision && !s.allowRollback {
		return fmt.Errorf("refusing configuration rollback from revision %d to %d", s.state.Config.Revision, config.Revision)
	}
	previous := s.state.Config
	previousRollback := s.allowRollback
	s.state.Config = cloneAgentConfig(config)
	// A rollback acceptance is one-shot: after any config write succeeds the
	// guard returns to its strict default so routine downgrades stay blocked.
	s.allowRollback = false
	if err := s.saveLocked(); err != nil {
		s.state.Config = previous
		s.allowRollback = previousRollback
		return err
	}
	return nil
}

// AcceptRollback allows the next SetConfig call to move to a lower revision.
// It exists for the documented control-plane restore procedure: after the
// database is restored from a backup its revision counter resets, and the
// Agent would otherwise refuse every new configuration until its local state
// file was deleted.
func (s *StateStore) AcceptRollback() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.allowRollback = true
}

func (s *StateStore) BeginRun(report domain.RunReport) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.state.RunKeys[report.IdempotencyKey]; exists {
		return false, nil
	}
	s.state.RunKeys[report.IdempotencyKey] = report.ID
	s.state.Runs[report.ID] = cloneReport(report)
	s.state.Outbox[report.ID] = cloneReport(report)
	if err := s.saveLocked(); err != nil {
		delete(s.state.RunKeys, report.IdempotencyKey)
		delete(s.state.Runs, report.ID)
		delete(s.state.Outbox, report.ID)
		return false, err
	}
	return true, nil
}

func (s *StateStore) FinishRun(report domain.RunReport) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previousRun, exists := s.state.Runs[report.ID]
	if !exists {
		return errors.New("cannot finish unknown run")
	}
	previousOutbox, hadOutbox := s.state.Outbox[report.ID]
	previousRejection, wasRejected := s.state.RejectedReports[report.ID]
	s.state.Runs[report.ID] = cloneReport(report)
	s.state.Outbox[report.ID] = cloneReport(report)
	delete(s.state.RejectedReports, report.ID)
	if err := s.saveLocked(); err != nil {
		s.state.Runs[report.ID] = previousRun
		if hadOutbox {
			s.state.Outbox[report.ID] = previousOutbox
		} else {
			delete(s.state.Outbox, report.ID)
		}
		if wasRejected {
			s.state.RejectedReports[report.ID] = previousRejection
		}
		return err
	}
	return nil
}

func (s *StateStore) PendingReports() []domain.RunReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]domain.RunReport, 0, len(s.state.Outbox))
	for _, report := range s.state.Outbox {
		result = append(result, cloneReport(report))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].StartedAt.Before(result[j].StartedAt) })
	return result
}

func (s *StateStore) AckReport(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := clonePersistedState(s.state)
	delete(s.state.Outbox, id)
	s.pruneHistoryLocked(2000)
	if err := s.saveLocked(); err != nil {
		s.state = previous
		return err
	}
	return nil
}

func (s *StateStore) QuarantineReport(id, reason string, rejectedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	report, exists := s.state.Outbox[id]
	if !exists {
		return errors.New("cannot quarantine unknown report")
	}
	previous := clonePersistedState(s.state)
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "control plane permanently rejected the report"
	}
	if len(reason) > maxReportRejectReason {
		reason = reason[:maxReportRejectReason]
	}
	if rejectedAt.IsZero() {
		rejectedAt = time.Now().UTC()
	}
	s.state.RejectedReports[id] = RejectedReport{
		Report:     cloneReport(report),
		Reason:     reason,
		RejectedAt: rejectedAt.UTC(),
	}
	delete(s.state.Outbox, id)
	s.pruneRejectedReportsLocked(maxRejectedReports)
	s.pruneHistoryLocked(2000)
	if err := s.saveLocked(); err != nil {
		s.state = previous
		return err
	}
	return nil
}

func (s *StateStore) RejectedReports() []RejectedReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]RejectedReport, 0, len(s.state.RejectedReports))
	for _, rejection := range s.state.RejectedReports {
		result = append(result, cloneRejectedReport(rejection))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].RejectedAt.After(result[j].RejectedAt) })
	return result
}

// QueueSnapshotInventory persists the latest snapshot index for a project,
// replacing any older undelivered inventory for the same project.
func (s *StateStore) QueueSnapshotInventory(projectID string, snapshots []domain.Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.SnapshotInventories == nil {
		s.state.SnapshotInventories = make(map[string]SnapshotInventory)
	}
	previous, hadPrevious := s.state.SnapshotInventories[projectID]
	s.state.SnapshotInventories[projectID] = SnapshotInventory{
		Snapshots: append([]domain.Snapshot(nil), snapshots...),
		QueuedAt:  time.Now().UTC(),
	}
	if err := s.saveLocked(); err != nil {
		if hadPrevious {
			s.state.SnapshotInventories[projectID] = previous
		} else {
			delete(s.state.SnapshotInventories, projectID)
		}
		return err
	}
	return nil
}

// PendingSnapshotInventory is one undelivered project index.
type PendingSnapshotInventory struct {
	ProjectID string
	Snapshots []domain.Snapshot
	QueuedAt  time.Time
}

// PendingSnapshotInventories returns one undelivered inventory per project,
// oldest delivery attempt first so newer indexes cannot overtake stale ones.
func (s *StateStore) PendingSnapshotInventories() []PendingSnapshotInventory {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]PendingSnapshotInventory, 0, len(s.state.SnapshotInventories))
	for projectID, inventory := range s.state.SnapshotInventories {
		result = append(result, PendingSnapshotInventory{
			ProjectID: projectID,
			Snapshots: append([]domain.Snapshot(nil), inventory.Snapshots...),
			QueuedAt:  inventory.QueuedAt,
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].QueuedAt.Before(result[j].QueuedAt) })
	return result
}

func (s *StateStore) AckSnapshotInventory(projectID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, hadPrevious := s.state.SnapshotInventories[projectID]
	delete(s.state.SnapshotInventories, projectID)
	if err := s.saveLocked(); err != nil {
		if hadPrevious {
			s.state.SnapshotInventories[projectID] = previous
		}
		return err
	}
	return nil
}

func (s *StateStore) initializeMaps() {
	if s.state.RunKeys == nil {
		s.state.RunKeys = make(map[string]string)
	}
	if s.state.Runs == nil {
		s.state.Runs = make(map[string]domain.RunReport)
	}
	if s.state.Outbox == nil {
		s.state.Outbox = make(map[string]domain.RunReport)
	}
	if s.state.RejectedReports == nil {
		s.state.RejectedReports = make(map[string]RejectedReport)
	}
	if s.state.SnapshotInventories == nil {
		s.state.SnapshotInventories = make(map[string]SnapshotInventory)
	}
}

func (s *StateStore) recoverInterruptedRuns() bool {
	changed := false
	now := time.Now().UTC()
	for id, report := range s.state.Runs {
		if report.Status != domain.RunRunning {
			continue
		}
		report.Status = domain.RunUnknown
		report.ErrorCode = "agent_restarted"
		report.ErrorMessage = "agent restarted before the backup process reached a known terminal state"
		report.FinishedAt = &now
		s.state.Runs[id] = report
		s.state.Outbox[id] = report
		delete(s.state.RejectedReports, id)
		changed = true
	}
	return changed
}

func (s *StateStore) saveLocked() error {
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode agent state: %w", err)
	}
	directory := filepath.Dir(s.path)
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open agent state directory: %w", err)
	}
	defer directoryHandle.Close()
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(s.path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary agent state: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("secure temporary agent state: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write agent state: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("flush agent state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close agent state: %w", err)
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return fmt.Errorf("replace agent state: %w", err)
	}
	// The state file itself is already durable. Directory fsync makes the rename
	// durable on filesystems that support it; after rename there is no safe
	// rollback, so a filesystem-specific sync error is best-effort only.
	_ = directoryHandle.Sync()
	return nil
}

func (s *StateStore) pruneHistoryLocked(limit int) {
	if len(s.state.Runs) <= limit {
		return
	}
	type historyItem struct {
		id       string
		finished time.Time
	}
	items := make([]historyItem, 0, len(s.state.Runs))
	for id, report := range s.state.Runs {
		if _, pending := s.state.Outbox[id]; pending || report.Status == domain.RunRunning {
			continue
		}
		finished := report.StartedAt
		if report.FinishedAt != nil {
			finished = *report.FinishedAt
		}
		items = append(items, historyItem{id: id, finished: finished})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].finished.Before(items[j].finished) })
	remove := len(s.state.Runs) - limit
	for _, item := range items {
		if remove <= 0 {
			break
		}
		report := s.state.Runs[item.id]
		delete(s.state.Runs, item.id)
		delete(s.state.RunKeys, report.IdempotencyKey)
		remove--
	}
}

func (s *StateStore) pruneRejectedReportsLocked(limit int) {
	if len(s.state.RejectedReports) <= limit {
		return
	}
	items := make([]RejectedReport, 0, len(s.state.RejectedReports))
	for _, rejection := range s.state.RejectedReports {
		items = append(items, rejection)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].RejectedAt.Before(items[j].RejectedAt) })
	for _, rejection := range items[:len(items)-limit] {
		delete(s.state.RejectedReports, rejection.Report.ID)
	}
}

func cloneAgentConfig(config domain.AgentConfig) domain.AgentConfig {
	data, _ := json.Marshal(config)
	var result domain.AgentConfig
	_ = json.Unmarshal(data, &result)
	return result
}

func cloneReport(report domain.RunReport) domain.RunReport {
	data, _ := json.Marshal(report)
	var result domain.RunReport
	_ = json.Unmarshal(data, &result)
	return result
}

func cloneRejectedReport(rejection RejectedReport) RejectedReport {
	return RejectedReport{
		Report:     cloneReport(rejection.Report),
		Reason:     rejection.Reason,
		RejectedAt: rejection.RejectedAt,
	}
}

func clonePersistedState(state persistedState) persistedState {
	result := persistedState{
		Config:              cloneAgentConfig(state.Config),
		RunKeys:             make(map[string]string, len(state.RunKeys)),
		Runs:                make(map[string]domain.RunReport, len(state.Runs)),
		Outbox:              make(map[string]domain.RunReport, len(state.Outbox)),
		RejectedReports:     make(map[string]RejectedReport, len(state.RejectedReports)),
		SnapshotInventories: make(map[string]SnapshotInventory, len(state.SnapshotInventories)),
	}
	if state.Identity != nil {
		identity := *state.Identity
		result.Identity = &identity
	}
	for key, id := range state.RunKeys {
		result.RunKeys[key] = id
	}
	for id, report := range state.Runs {
		result.Runs[id] = cloneReport(report)
	}
	for id, report := range state.Outbox {
		result.Outbox[id] = cloneReport(report)
	}
	for id, rejection := range state.RejectedReports {
		result.RejectedReports[id] = cloneRejectedReport(rejection)
	}
	for projectID, inventory := range state.SnapshotInventories {
		result.SnapshotInventories[projectID] = SnapshotInventory{
			Snapshots: append([]domain.Snapshot(nil), inventory.Snapshots...),
			QueuedAt:  inventory.QueuedAt,
		}
	}
	return result
}
