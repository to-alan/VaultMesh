package memory

import (
	"context"
	"encoding/hex"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/to-alan/vaultmesh/internal/domain"
	"github.com/to-alan/vaultmesh/internal/store"
)

type enrollment struct {
	serverID string
	expires  time.Time
	used     bool
}

type Store struct {
	mu           sync.RWMutex
	admin        *domain.AdminAccount
	servers      map[string]domain.Server
	enrollments  map[string]enrollment
	credentials  map[string]string
	repositories map[string]domain.Repository
	projects     map[string]domain.Project
	commands     map[string]domain.Command
	accepted     map[string]time.Time
	completed    map[string]time.Time
	runs         map[string]domain.RunReport
	runKeys      map[string]string
}

func New() *Store {
	return &Store{
		servers:      make(map[string]domain.Server),
		enrollments:  make(map[string]enrollment),
		credentials:  make(map[string]string),
		repositories: make(map[string]domain.Repository),
		projects:     make(map[string]domain.Project),
		commands:     make(map[string]domain.Command),
		accepted:     make(map[string]time.Time),
		completed:    make(map[string]time.Time),
		runs:         make(map[string]domain.RunReport),
		runKeys:      make(map[string]string),
	}
}

func (s *Store) Ping(context.Context) error { return nil }
func (s *Store) Close()                     {}

func (s *Store) GetAdminAccount(context.Context) (domain.AdminAccount, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.admin == nil {
		return domain.AdminAccount{}, store.ErrNotFound
	}
	return cloneAdminAccount(*s.admin), nil
}

func (s *Store) SaveAdminAccount(_ context.Context, account domain.AdminAccount) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cloned := cloneAdminAccount(account)
	s.admin = &cloned
	return nil
}

func (s *Store) CreateServer(_ context.Context, server domain.Server, tokenHash []byte, expires time.Time) (domain.Server, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.servers[server.ID]; exists {
		return domain.Server{}, store.ErrConflict
	}
	server.Status = domain.ServerPending
	s.servers[server.ID] = server
	s.enrollments[key(tokenHash)] = enrollment{serverID: server.ID, expires: expires}
	return server, nil
}

func (s *Store) EnrollAgent(_ context.Context, enrollmentHash, credentialHash []byte, info domain.AgentInfo) (domain.Server, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	enrollmentKey := key(enrollmentHash)
	entry, ok := s.enrollments[enrollmentKey]
	if !ok || entry.used || time.Now().After(entry.expires) {
		return domain.Server{}, store.ErrInvalidEnrollment
	}
	server, ok := s.servers[entry.serverID]
	if !ok {
		return domain.Server{}, store.ErrInvalidEnrollment
	}
	entry.used = true
	s.enrollments[enrollmentKey] = entry
	s.credentials[key(credentialHash)] = server.ID
	now := time.Now().UTC()
	server.Hostname = info.Hostname
	server.OS = info.OS
	server.Arch = info.Arch
	server.AgentVersion = info.AgentVersion
	server.Status = domain.ServerOnline
	server.LastSeenAt = &now
	s.servers[server.ID] = server
	return server, nil
}

func (s *Store) AuthenticateAgent(_ context.Context, credentialHash []byte) (domain.Server, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	serverID, ok := s.credentials[key(credentialHash)]
	if !ok {
		return domain.Server{}, store.ErrUnauthorized
	}
	server, ok := s.servers[serverID]
	if !ok {
		return domain.Server{}, store.ErrUnauthorized
	}
	return server, nil
}

func (s *Store) UpdateHeartbeat(_ context.Context, serverID string, heartbeat domain.Heartbeat, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	server, ok := s.servers[serverID]
	if !ok {
		return store.ErrNotFound
	}
	server.Hostname = heartbeat.Hostname
	server.OS = heartbeat.OS
	server.Arch = heartbeat.Arch
	server.AgentVersion = heartbeat.AgentVersion
	server.AppliedRevision = heartbeat.AppliedRevision
	server.Status = domain.ServerOnline
	at = at.UTC()
	server.LastSeenAt = &at
	s.servers[serverID] = server
	return nil
}

func (s *Store) ListServers(context.Context) ([]domain.Server, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]domain.Server, 0, len(s.servers))
	for _, server := range s.servers {
		if server.LastSeenAt != nil && time.Since(*server.LastSeenAt) > 90*time.Second {
			server.Status = domain.ServerOffline
		}
		result = append(result, server)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result, nil
}

func (s *Store) CreateRepository(_ context.Context, repository domain.Repository) (domain.Repository, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.repositories[repository.ID]; ok {
		return domain.Repository{}, store.ErrConflict
	}
	s.repositories[repository.ID] = cloneRepository(repository)
	return publicRepository(repository), nil
}

func (s *Store) ListRepositories(context.Context) ([]domain.Repository, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]domain.Repository, 0, len(s.repositories))
	for _, repository := range s.repositories {
		result = append(result, publicRepository(repository))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result, nil
}

func (s *Store) GetRepository(_ context.Context, id string) (domain.Repository, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	repository, ok := s.repositories[id]
	if !ok {
		return domain.Repository{}, store.ErrNotFound
	}
	return cloneRepository(repository), nil
}

func (s *Store) CreateProject(_ context.Context, project domain.Project) (domain.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	server, ok := s.servers[project.ServerID]
	if !ok {
		return domain.Project{}, store.ErrNotFound
	}
	if _, ok := s.repositories[project.RepositoryID]; !ok {
		return domain.Project{}, store.ErrNotFound
	}
	if _, ok := s.projects[project.ID]; ok {
		return domain.Project{}, store.ErrConflict
	}
	server.DesiredRevision++
	project.Revision = server.DesiredRevision
	s.servers[server.ID] = server
	s.projects[project.ID] = cloneProject(project)
	return cloneProject(project), nil
}

func (s *Store) ListProjects(context.Context) ([]domain.Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]domain.Project, 0, len(s.projects))
	for _, project := range s.projects {
		result = append(result, cloneProject(project))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result, nil
}

func (s *Store) SetProjectEnabled(_ context.Context, id string, enabled bool, updatedAt time.Time) (domain.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	project, ok := s.projects[id]
	if !ok {
		return domain.Project{}, store.ErrNotFound
	}
	server, ok := s.servers[project.ServerID]
	if !ok {
		return domain.Project{}, store.ErrNotFound
	}
	if project.Enabled == enabled {
		return cloneProject(project), nil
	}
	server.DesiredRevision++
	project.Enabled = enabled
	project.Revision = server.DesiredRevision
	project.UpdatedAt = updatedAt
	s.servers[server.ID] = server
	s.projects[id] = cloneProject(project)
	return cloneProject(project), nil
}

func (s *Store) DesiredConfig(_ context.Context, serverID string) (domain.AgentConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	server, ok := s.servers[serverID]
	if !ok {
		return domain.AgentConfig{}, store.ErrNotFound
	}
	config := domain.AgentConfig{Revision: server.DesiredRevision}
	for _, project := range s.projects {
		if project.ServerID != serverID || !project.Enabled {
			continue
		}
		repository := s.repositories[project.RepositoryID]
		config.Projects = append(config.Projects, domain.AgentProject{
			Project:    cloneProject(project),
			Repository: cloneRepository(repository),
		})
	}
	sort.Slice(config.Projects, func(i, j int) bool { return config.Projects[i].ID < config.Projects[j].ID })
	return config, nil
}

func (s *Store) CreateCommand(_ context.Context, command domain.Command) (domain.Command, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	project, ok := s.projects[command.ProjectID]
	if !ok || !project.Enabled {
		return domain.Command{}, store.ErrNotFound
	}
	if _, exists := s.commands[command.ID]; exists {
		return domain.Command{}, store.ErrConflict
	}
	command.ServerID = project.ServerID
	s.commands[command.ID] = command
	return command, nil
}

func (s *Store) ClaimCommands(_ context.Context, serverID string, now, leaseUntil time.Time, limit int) ([]domain.Command, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 || limit > 20 {
		limit = 10
	}
	var candidates []domain.Command
	for id, command := range s.commands {
		if command.ServerID != serverID {
			continue
		}
		if _, accepted := s.accepted[id]; accepted {
			continue
		}
		if command.LeaseUntil != nil && command.LeaseUntil.After(now) {
			continue
		}
		candidates = append(candidates, command)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].CreatedAt.Before(candidates[j].CreatedAt) })
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	for index := range candidates {
		command := candidates[index]
		lease := leaseUntil.UTC()
		command.LeaseUntil = &lease
		command.Attempts++
		s.commands[command.ID] = command
		candidates[index] = command
	}
	return candidates, nil
}

func (s *Store) UpsertRun(_ context.Context, report domain.RunReport) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existingID, ok := s.runKeys[report.IdempotencyKey]; ok && existingID != report.ID {
		return store.ErrConflict
	}
	project, ok := s.projects[report.ProjectID]
	if !ok || project.ServerID != report.ServerID {
		return store.ErrNotFound
	}
	s.runKeys[report.IdempotencyKey] = report.ID
	s.runs[report.ID] = cloneRun(report)
	if commandID, ok := strings.CutPrefix(report.IdempotencyKey, "manual:"); ok {
		now := time.Now().UTC()
		s.accepted[commandID] = now
		if report.Status != domain.RunRunning {
			s.completed[commandID] = now
		}
	}
	return nil
}

func (s *Store) ListRuns(_ context.Context, limit int) ([]domain.RunReport, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]domain.RunReport, 0, len(s.runs))
	for _, report := range s.runs {
		result = append(result, cloneRun(report))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].StartedAt.After(result[j].StartedAt) })
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (s *Store) Dashboard(_ context.Context, since time.Time) (domain.Dashboard, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	dashboard := domain.Dashboard{ServersTotal: len(s.servers), ProjectsTotal: len(s.projects)}
	for _, server := range s.servers {
		if server.LastSeenAt != nil && time.Since(*server.LastSeenAt) <= 90*time.Second {
			dashboard.ServersOnline++
		}
	}
	for _, report := range s.runs {
		if report.StartedAt.Before(since) {
			continue
		}
		if operation, _ := report.Stats["operation"].(string); operation != "" && operation != "backup" {
			continue
		}
		switch report.Status {
		case domain.RunSucceeded:
			dashboard.RunsSucceeded++
		case domain.RunPartial:
			dashboard.RunsPartial++
		case domain.RunFailed, domain.RunTimedOut, domain.RunUnknown:
			dashboard.RunsFailed++
		}
	}
	return dashboard, nil
}

func key(value []byte) string { return hex.EncodeToString(value) }

func cloneAdminAccount(account domain.AdminAccount) domain.AdminAccount {
	account.PasswordHash = append([]byte(nil), account.PasswordHash...)
	account.WebAuthnUserID = append([]byte(nil), account.WebAuthnUserID...)
	account.SecurityData = append([]byte(nil), account.SecurityData...)
	return account
}

func publicRepository(repository domain.Repository) domain.Repository {
	repository.Password = ""
	repository.Environment = nil
	repository.Options = nil
	repository.SecretCiphertext = nil
	return repository
}

func cloneRepository(repository domain.Repository) domain.Repository {
	repository.SecretCiphertext = append([]byte(nil), repository.SecretCiphertext...)
	if repository.Environment != nil {
		repository.Environment = cloneMap(repository.Environment)
	}
	if repository.Options != nil {
		repository.Options = cloneMap(repository.Options)
	}
	return repository
}

func cloneProject(project domain.Project) domain.Project {
	project.Sources = append([]domain.Source(nil), project.Sources...)
	for i := range project.Sources {
		project.Sources[i].Paths = append([]string(nil), project.Sources[i].Paths...)
		project.Sources[i].Excludes = append([]string(nil), project.Sources[i].Excludes...)
		if project.Sources[i].Database != nil {
			database := *project.Sources[i].Database
			project.Sources[i].Database = &database
		}
		if project.Sources[i].Docker != nil {
			docker := *project.Sources[i].Docker
			docker.Containers = append([]string(nil), docker.Containers...)
			project.Sources[i].Docker = &docker
		}
	}
	project.Policy.Backup.ExcludeIfPresent = append([]string(nil), project.Policy.Backup.ExcludeIfPresent...)
	return project
}

func cloneRun(report domain.RunReport) domain.RunReport {
	if report.Stats != nil {
		report.Stats = cloneAnyMap(report.Stats)
	}
	return report
}

func cloneMap(input map[string]string) map[string]string {
	output := make(map[string]string, len(input))
	for k, v := range input {
		output[k] = v
	}
	return output
}

func cloneAnyMap(input map[string]any) map[string]any {
	output := make(map[string]any, len(input))
	for k, v := range input {
		output[k] = v
	}
	return output
}
