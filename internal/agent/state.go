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

type persistedState struct {
	Identity *domain.AgentIdentity       `json:"identity,omitempty"`
	Config   domain.AgentConfig          `json:"config"`
	RunKeys  map[string]string           `json:"run_keys"`
	Runs     map[string]domain.RunReport `json:"runs"`
	Outbox   map[string]domain.RunReport `json:"outbox"`
}

type StateStore struct {
	mu    sync.Mutex
	path  string
	state persistedState
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
	if config.Revision < s.state.Config.Revision {
		return fmt.Errorf("refusing configuration rollback from revision %d to %d", s.state.Config.Revision, config.Revision)
	}
	previous := s.state.Config
	s.state.Config = cloneAgentConfig(config)
	if err := s.saveLocked(); err != nil {
		s.state.Config = previous
		return err
	}
	return nil
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
	s.state.Runs[report.ID] = cloneReport(report)
	s.state.Outbox[report.ID] = cloneReport(report)
	if err := s.saveLocked(); err != nil {
		s.state.Runs[report.ID] = previousRun
		if hadOutbox {
			s.state.Outbox[report.ID] = previousOutbox
		} else {
			delete(s.state.Outbox, report.ID)
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

func clonePersistedState(state persistedState) persistedState {
	result := persistedState{
		Config:  cloneAgentConfig(state.Config),
		RunKeys: make(map[string]string, len(state.RunKeys)),
		Runs:    make(map[string]domain.RunReport, len(state.Runs)),
		Outbox:  make(map[string]domain.RunReport, len(state.Outbox)),
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
	return result
}
