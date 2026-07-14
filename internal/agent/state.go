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
	copy := identity
	s.state.Identity = &copy
	return s.saveLocked()
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
	s.state.Config = cloneAgentConfig(config)
	return s.saveLocked()
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
	if _, exists := s.state.Runs[report.ID]; !exists {
		return errors.New("cannot finish unknown run")
	}
	s.state.Runs[report.ID] = cloneReport(report)
	s.state.Outbox[report.ID] = cloneReport(report)
	return s.saveLocked()
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
	delete(s.state.Outbox, id)
	s.pruneHistoryLocked(2000)
	return s.saveLocked()
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
	temporary := s.path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return fmt.Errorf("write agent state: %w", err)
	}
	if err := os.Rename(temporary, s.path); err != nil {
		return fmt.Errorf("replace agent state: %w", err)
	}
	return os.Chmod(s.path, 0o600)
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
