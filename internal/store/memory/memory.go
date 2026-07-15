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
	snapshots    map[string]map[string]domain.Snapshot
	snapshotSync map[string]time.Time
	accepted     map[string]time.Time
	completed    map[string]time.Time
	runs         map[string]domain.RunReport
	runKeys      map[string]string
	channels     map[string]domain.NotificationChannel
	alerts       map[string]domain.AlertIncident
	deliveries   map[string]domain.NotificationDelivery
	deliveryKeys map[string]string
	auditEvents  []domain.AuditEvent
	auditIDs     map[string]struct{}
}

func New() *Store {
	return &Store{
		servers:      make(map[string]domain.Server),
		enrollments:  make(map[string]enrollment),
		credentials:  make(map[string]string),
		repositories: make(map[string]domain.Repository),
		projects:     make(map[string]domain.Project),
		commands:     make(map[string]domain.Command),
		snapshots:    make(map[string]map[string]domain.Snapshot),
		snapshotSync: make(map[string]time.Time),
		accepted:     make(map[string]time.Time),
		completed:    make(map[string]time.Time),
		runs:         make(map[string]domain.RunReport),
		runKeys:      make(map[string]string),
		channels:     make(map[string]domain.NotificationChannel),
		alerts:       make(map[string]domain.AlertIncident),
		deliveries:   make(map[string]domain.NotificationDelivery),
		deliveryKeys: make(map[string]string),
		auditIDs:     make(map[string]struct{}),
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

func (s *Store) GetProject(_ context.Context, id string) (domain.Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	project, ok := s.projects[id]
	if !ok {
		return domain.Project{}, store.ErrNotFound
	}
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

func (s *Store) UpdateProject(_ context.Context, project domain.Project, updatedAt time.Time) (domain.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.projects[project.ID]
	if !ok {
		return domain.Project{}, store.ErrNotFound
	}
	if project.ServerID != current.ServerID || project.RepositoryID != current.RepositoryID {
		return domain.Project{}, store.ErrConflict
	}
	for id, candidate := range s.projects {
		if id != project.ID && candidate.ServerID == project.ServerID && candidate.Name == project.Name {
			return domain.Project{}, store.ErrConflict
		}
	}
	server, ok := s.servers[current.ServerID]
	if !ok {
		return domain.Project{}, store.ErrNotFound
	}
	server.DesiredRevision++
	project.Enabled = current.Enabled
	project.Revision = server.DesiredRevision
	project.CreatedAt = current.CreatedAt
	project.UpdatedAt = updatedAt
	s.servers[server.ID] = server
	s.projects[project.ID] = cloneProject(project)
	return cloneProject(project), nil
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
	command.Payload = cloneAnyMap(command.Payload)
	s.commands[command.ID] = command
	return cloneCommand(command), nil
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
		candidates = append(candidates, cloneCommand(command))
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

func (s *Store) ReplaceProjectSnapshots(_ context.Context, projectID, serverID string, snapshots []domain.Snapshot, syncedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	project, ok := s.projects[projectID]
	if !ok || project.ServerID != serverID {
		return store.ErrNotFound
	}
	if latestSync := s.snapshotSync[projectID]; !latestSync.IsZero() && !syncedAt.After(latestSync) {
		return nil
	}
	items := make(map[string]domain.Snapshot, len(snapshots))
	for _, snapshot := range snapshots {
		if snapshot.ID == "" {
			continue
		}
		snapshot.ProjectID = projectID
		snapshot.ServerID = serverID
		snapshot.LastSyncedAt = syncedAt
		items[snapshot.ID] = cloneSnapshot(snapshot)
	}
	s.snapshots[projectID] = items
	s.snapshotSync[projectID] = syncedAt
	return nil
}

func (s *Store) ListSnapshots(_ context.Context, projectID string, limit int) ([]domain.Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if projectID != "" {
		if _, ok := s.projects[projectID]; !ok {
			return nil, store.ErrNotFound
		}
	}
	var result []domain.Snapshot
	for id, items := range s.snapshots {
		if projectID != "" && id != projectID {
			continue
		}
		for _, snapshot := range items {
			result = append(result, cloneSnapshot(snapshot))
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Time.After(result[j].Time) })
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (s *Store) GetSnapshot(_ context.Context, projectID, snapshotID string) (domain.Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snapshot, ok := s.snapshots[projectID][snapshotID]
	if !ok {
		return domain.Snapshot{}, store.ErrNotFound
	}
	return cloneSnapshot(snapshot), nil
}

func (s *Store) UpsertRun(_ context.Context, report domain.RunReport) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	terminalDuplicate := false
	if existingID, ok := s.runKeys[report.IdempotencyKey]; ok && existingID != report.ID {
		return store.ErrConflict
	}
	if existing, ok := s.runs[report.ID]; ok {
		if existing.IdempotencyKey != report.IdempotencyKey || existing.ProjectID != report.ProjectID || existing.ServerID != report.ServerID {
			return store.ErrConflict
		}
		if existing.Status != domain.RunRunning {
			// Terminal run facts are immutable. A delayed duplicate running report
			// is acknowledged without regressing the stored result. Continue so a
			// related manual command can still be repaired idempotently.
			terminalDuplicate = true
		}
	}
	if !terminalDuplicate {
		project, ok := s.projects[report.ProjectID]
		if !ok || project.ServerID != report.ServerID {
			return store.ErrNotFound
		}
		s.runKeys[report.IdempotencyKey] = report.ID
		s.runs[report.ID] = cloneRun(report)
	}
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
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (s *Store) ListProjectBackupActivity(_ context.Context) ([]domain.ProjectBackupActivity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	activity := make(map[string]domain.ProjectBackupActivity, len(s.projects))
	for id := range s.projects {
		activity[id] = domain.ProjectBackupActivity{ProjectID: id}
	}
	for _, report := range s.runs {
		operation, _ := report.Stats["operation"].(string)
		if operation != "" && operation != "backup" {
			continue
		}
		item, ok := activity[report.ProjectID]
		if !ok {
			continue
		}
		if item.LatestRunAt == nil || report.StartedAt.After(*item.LatestRunAt) {
			startedAt := report.StartedAt
			item.LatestRunAt = &startedAt
			item.LatestRunID = report.ID
			item.LatestRunStatus = report.Status
		}
		if report.Status == domain.RunSucceeded {
			succeededAt := report.StartedAt
			if report.FinishedAt != nil {
				succeededAt = *report.FinishedAt
			}
			if item.LastSuccessfulAt == nil || succeededAt.After(*item.LastSuccessfulAt) {
				item.LastSuccessfulAt = &succeededAt
			}
		}
		activity[report.ProjectID] = item
	}
	result := make([]domain.ProjectBackupActivity, 0, len(activity))
	for _, item := range activity {
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ProjectID < result[j].ProjectID })
	return result, nil
}

func (s *Store) CreateNotificationChannel(_ context.Context, channel domain.NotificationChannel) (domain.NotificationChannel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.channels {
		if existing.DeletedAt == nil && existing.Name == channel.Name {
			return domain.NotificationChannel{}, store.ErrConflict
		}
	}
	if _, exists := s.channels[channel.ID]; exists {
		return domain.NotificationChannel{}, store.ErrConflict
	}
	s.channels[channel.ID] = cloneNotificationChannel(channel)
	return cloneNotificationChannel(channel), nil
}

func (s *Store) GetNotificationChannel(_ context.Context, id string) (domain.NotificationChannel, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	channel, ok := s.channels[id]
	if !ok {
		return domain.NotificationChannel{}, store.ErrNotFound
	}
	return cloneNotificationChannel(channel), nil
}

func (s *Store) ListNotificationChannels(context.Context) ([]domain.NotificationChannel, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]domain.NotificationChannel, 0, len(s.channels))
	for _, channel := range s.channels {
		if channel.DeletedAt == nil {
			result = append(result, cloneNotificationChannel(channel))
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result, nil
}

func (s *Store) UpdateNotificationChannel(_ context.Context, channel domain.NotificationChannel) (domain.NotificationChannel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.channels[channel.ID]
	if !ok || current.DeletedAt != nil {
		return domain.NotificationChannel{}, store.ErrNotFound
	}
	for id, existing := range s.channels {
		if id != channel.ID && existing.DeletedAt == nil && existing.Name == channel.Name {
			return domain.NotificationChannel{}, store.ErrConflict
		}
	}
	channel.CreatedAt = current.CreatedAt
	s.channels[channel.ID] = cloneNotificationChannel(channel)
	return cloneNotificationChannel(channel), nil
}

func (s *Store) ArchiveNotificationChannel(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, ok := s.channels[id]
	if !ok || channel.DeletedAt != nil {
		return store.ErrNotFound
	}
	channel.Enabled = false
	channel.DeletedAt = &at
	channel.UpdatedAt = at
	s.channels[id] = channel
	return nil
}

func (s *Store) CreateAlertIncident(_ context.Context, alert domain.AlertIncident) (domain.AlertIncident, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.alerts {
		if existing.Fingerprint == alert.Fingerprint && existing.Status == "firing" {
			return domain.AlertIncident{}, store.ErrConflict
		}
	}
	if _, exists := s.alerts[alert.ID]; exists {
		return domain.AlertIncident{}, store.ErrConflict
	}
	s.alerts[alert.ID] = alert
	return alert, nil
}

func (s *Store) GetAlertIncident(_ context.Context, id string) (domain.AlertIncident, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	alert, ok := s.alerts[id]
	if !ok {
		return domain.AlertIncident{}, store.ErrNotFound
	}
	return alert, nil
}

func (s *Store) GetFiringAlertIncident(_ context.Context, fingerprint string) (domain.AlertIncident, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, alert := range s.alerts {
		if alert.Fingerprint == fingerprint && alert.Status == "firing" {
			return alert, nil
		}
	}
	return domain.AlertIncident{}, store.ErrNotFound
}

func (s *Store) UpdateAlertIncident(_ context.Context, alert domain.AlertIncident) (domain.AlertIncident, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.alerts[alert.ID]; !ok {
		return domain.AlertIncident{}, store.ErrNotFound
	}
	s.alerts[alert.ID] = alert
	return alert, nil
}

func (s *Store) ListAlertIncidents(_ context.Context, limit int) ([]domain.AlertIncident, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	result := make([]domain.AlertIncident, 0, len(s.alerts))
	for _, alert := range s.alerts {
		result = append(result, alert)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].UpdatedAt.After(result[j].UpdatedAt) })
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (s *Store) CreateNotificationDelivery(_ context.Context, delivery domain.NotificationDelivery) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.deliveryKeys[delivery.DedupeKey]; exists {
		return store.ErrConflict
	}
	if _, exists := s.deliveries[delivery.ID]; exists {
		return store.ErrConflict
	}
	s.deliveryKeys[delivery.DedupeKey] = delivery.ID
	s.deliveries[delivery.ID] = delivery
	return nil
}

func (s *Store) ClaimNotificationDeliveries(_ context.Context, now, leaseUntil time.Time, limit int) ([]domain.NotificationDelivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	var candidates []domain.NotificationDelivery
	for _, delivery := range s.deliveries {
		eligible := delivery.Status == "pending" && !delivery.NextAttemptAt.After(now)
		eligible = eligible || (delivery.Status == "delivering" && delivery.LeaseUntil != nil && !delivery.LeaseUntil.After(now))
		if eligible {
			candidates = append(candidates, delivery)
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].CreatedAt.Before(candidates[j].CreatedAt) })
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	for index := range candidates {
		delivery := candidates[index]
		delivery.Status = "delivering"
		delivery.AttemptCount++
		delivery.LeaseUntil = &leaseUntil
		s.deliveries[delivery.ID] = delivery
		candidates[index] = delivery
	}
	return candidates, nil
}

func (s *Store) CompleteNotificationDelivery(_ context.Context, id string, sent bool, lastError string, completedAt, nextAttemptAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delivery, ok := s.deliveries[id]
	if !ok {
		return store.ErrNotFound
	}
	delivery.LeaseUntil = nil
	delivery.LastError = lastError
	if sent {
		delivery.Status = "sent"
		delivery.SentAt = &completedAt
	} else if nextAttemptAt.IsZero() {
		delivery.Status = "failed"
	} else {
		delivery.Status = "pending"
		delivery.NextAttemptAt = nextAttemptAt
	}
	s.deliveries[id] = delivery
	return nil
}

func (s *Store) ListNotificationDeliveries(_ context.Context, limit int) ([]domain.NotificationDelivery, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	result := make([]domain.NotificationDelivery, 0, len(s.deliveries))
	for _, delivery := range s.deliveries {
		delivery.ChannelName = s.channels[delivery.ChannelID].Name
		result = append(result, delivery)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (s *Store) AppendAuditEvent(_ context.Context, event domain.AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.auditIDs[event.ID]; exists {
		return store.ErrConflict
	}
	s.auditIDs[event.ID] = struct{}{}
	s.auditEvents = append(s.auditEvents, event)
	return nil
}

func (s *Store) ListAuditEvents(_ context.Context, limit int) ([]domain.AuditEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	result := append([]domain.AuditEvent(nil), s.auditEvents...)
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	if len(result) > limit {
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

func cloneNotificationChannel(channel domain.NotificationChannel) domain.NotificationChannel {
	channel.SecretCiphertext = append([]byte(nil), channel.SecretCiphertext...)
	channel.EventTypes = append([]string(nil), channel.EventTypes...)
	channel.ProjectIDs = append([]string(nil), channel.ProjectIDs...)
	channel.Config = cloneMap(channel.Config)
	return channel
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

func cloneCommand(command domain.Command) domain.Command {
	command.Payload = cloneAnyMap(command.Payload)
	return command
}

func cloneSnapshot(snapshot domain.Snapshot) domain.Snapshot {
	snapshot.Paths = append([]string(nil), snapshot.Paths...)
	snapshot.Tags = append([]string(nil), snapshot.Tags...)
	return snapshot
}

func cloneMap(input map[string]string) map[string]string {
	output := make(map[string]string, len(input))
	for k, v := range input {
		output[k] = v
	}
	return output
}

func cloneAnyMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	output := make(map[string]any, len(input))
	for k, v := range input {
		output[k] = v
	}
	return output
}
