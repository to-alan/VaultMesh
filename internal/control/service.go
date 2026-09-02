package control

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/to-alan/vaultmesh/internal/domain"
	"github.com/to-alan/vaultmesh/internal/schedule"
	"github.com/to-alan/vaultmesh/internal/secret"
	"github.com/to-alan/vaultmesh/internal/store"
)

const enrollmentTTL = 15 * time.Minute
const protectedSnapshotTag = "vaultmesh.protected=true"
const defaultScheduleGrace = time.Hour

var fullResticSnapshotID = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Service struct {
	store              store.Store
	sealer             *secret.Sealer
	now                func() time.Time
	notificationSender notificationSender
	dataRetention      domain.DataRetention
}

func NewService(dataStore store.Store, sealer *secret.Sealer) *Service {
	return &Service{
		store: dataStore, sealer: sealer,
		now:                func() time.Time { return time.Now().UTC() },
		notificationSender: sendNotification,
	}
}

// SetDataRetention configures how long finished runs, completed commands,
// resolved incidents, delivery history, and audit events are kept. Zero days
// disables pruning for that scope.
func (s *Service) SetDataRetention(retention domain.DataRetention) {
	s.dataRetention = retention
}

func (s *Service) CreateServer(ctx context.Context, name string) (domain.EnrollmentResult, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return domain.EnrollmentResult{}, validationError("name", "must contain 1 to 100 characters")
	}
	id, err := randomValue("srv", 10)
	if err != nil {
		return domain.EnrollmentResult{}, err
	}
	token, err := randomValue("enroll", 32)
	if err != nil {
		return domain.EnrollmentResult{}, err
	}
	now := s.now()
	expiresAt := now.Add(enrollmentTTL)
	server, err := s.store.CreateServer(ctx, domain.Server{
		ID:        id,
		Name:      name,
		Status:    domain.ServerPending,
		CreatedAt: now,
	}, hashToken(token), expiresAt)
	if err != nil {
		return domain.EnrollmentResult{}, err
	}
	return domain.EnrollmentResult{Server: server, EnrollmentToken: token, ExpiresAt: expiresAt}, nil
}

func (s *Service) EnrollAgent(ctx context.Context, enrollmentToken string, info domain.AgentInfo) (domain.AgentIdentity, error) {
	enrollmentToken = strings.TrimSpace(enrollmentToken)
	if enrollmentToken == "" {
		return domain.AgentIdentity{}, store.ErrInvalidEnrollment
	}
	if strings.TrimSpace(info.Hostname) == "" {
		return domain.AgentIdentity{}, validationError("hostname", "is required")
	}
	deviceToken, err := randomValue("agent", 32)
	if err != nil {
		return domain.AgentIdentity{}, err
	}
	server, err := s.store.EnrollAgent(ctx, hashToken(enrollmentToken), hashToken(deviceToken), info)
	if err != nil {
		return domain.AgentIdentity{}, err
	}
	return domain.AgentIdentity{AgentID: server.ID, Token: deviceToken}, nil
}

func (s *Service) AuthenticateAgent(ctx context.Context, token string) (domain.Server, error) {
	if token == "" {
		return domain.Server{}, store.ErrUnauthorized
	}
	return s.store.AuthenticateAgent(ctx, hashToken(token))
}

func (s *Service) Heartbeat(ctx context.Context, serverID string, heartbeat domain.Heartbeat) error {
	return s.store.UpdateHeartbeat(ctx, serverID, heartbeat, s.now())
}

func (s *Service) CreateProject(ctx context.Context, input domain.Project) (domain.Project, error) {
	if err := s.prepareProject(&input, nil); err != nil {
		return domain.Project{}, err
	}
	id, err := randomValue("prj", 10)
	if err != nil {
		return domain.Project{}, err
	}
	now := s.now()
	input.ID = id
	input.Enabled = true
	input.CreatedAt = now
	input.UpdatedAt = now
	created, err := s.store.CreateProject(ctx, input)
	if err != nil {
		return domain.Project{}, err
	}
	return publicProject(created, s.now()), nil
}

func (s *Service) UpdateProject(ctx context.Context, projectID string, input domain.Project) (domain.Project, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return domain.Project{}, validationError("project_id", "is required")
	}
	existing, err := s.store.GetProject(ctx, projectID)
	if err != nil {
		return domain.Project{}, err
	}
	if input.ServerID != "" && input.ServerID != existing.ServerID {
		return domain.Project{}, validationError("server_id", "cannot be changed; create a new project to move backup ownership")
	}
	if input.RepositoryID != "" && input.RepositoryID != existing.RepositoryID {
		return domain.Project{}, validationError("repository_id", "cannot be changed; create a new project to preserve the old recovery chain")
	}
	input.ID = existing.ID
	input.ServerID = existing.ServerID
	input.RepositoryID = existing.RepositoryID
	input.Enabled = existing.Enabled
	input.CreatedAt = existing.CreatedAt
	if err := s.prepareProject(&input, &existing); err != nil {
		return domain.Project{}, err
	}
	updated, err := s.store.UpdateProject(ctx, input, s.now())
	if err != nil {
		return domain.Project{}, err
	}
	return publicProject(updated, s.now()), nil
}

func (s *Service) prepareProject(input *domain.Project, existing *domain.Project) error {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 100 {
		return validationError("name", "must contain 1 to 100 characters")
	}
	if input.ServerID == "" || input.RepositoryID == "" {
		return validationError("server_id", "server_id and repository_id are required")
	}
	if len(input.Sources) == 0 {
		return validationError("sources", "at least one source is required")
	}
	existingSources := make(map[string]domain.Source)
	if existing != nil {
		for _, source := range existing.Sources {
			existingSources[source.ID] = source
		}
	}
	seenSourceIDs := make(map[string]struct{}, len(input.Sources))
	for index := range input.Sources {
		source := &input.Sources[index]
		if source.ID == "" {
			id, err := randomValue("src", 8)
			if err != nil {
				return err
			}
			source.ID = id
		}
		if _, duplicated := seenSourceIDs[source.ID]; duplicated {
			return validationError("sources.id", "source IDs must be unique")
		}
		seenSourceIDs[source.ID] = struct{}{}
		switch source.Type {
		case "files":
			if len(source.Paths) == 0 {
				return validationError("sources.paths", "at least one path is required")
			}
			for pathIndex, path := range source.Paths {
				cleaned, err := validateSourcePath(path)
				if err != nil {
					return validationError("sources.paths", err.Error())
				}
				source.Paths[pathIndex] = cleaned
			}
			source.Database = nil
			source.Docker = nil
			source.SecretCiphertext = ""
		case "mysql", "postgresql":
			existingSecret := ""
			if previous, ok := existingSources[source.ID]; ok && previous.Type == source.Type {
				existingSecret = previous.SecretCiphertext
			}
			if err := s.prepareDatabaseSource(source, existingSecret); err != nil {
				return err
			}
		case "docker":
			if err := prepareDockerSource(source); err != nil {
				return err
			}
			source.SecretCiphertext = ""
		default:
			return validationError("sources.type", "must be files, mysql, postgresql, or docker")
		}
	}
	if input.Schedule.Timezone == "" {
		input.Schedule.Timezone = "UTC"
	}
	if input.Schedule.MissedRunPolicy == "" {
		input.Schedule.MissedRunPolicy = "skip"
	}
	if input.Schedule.ConcurrencyPolicy == "" {
		input.Schedule.ConcurrencyPolicy = "forbid"
	}
	if input.Schedule.MaxRuntimeSeconds == 0 {
		input.Schedule.MaxRuntimeSeconds = 6 * 60 * 60
	}
	if input.Schedule.GraceSeconds == 0 {
		input.Schedule.GraceSeconds = 60 * 60
	}
	if input.Schedule.JitterSeconds < 0 || input.Schedule.JitterSeconds > 3600 {
		return validationError("schedule.jitter_seconds", "must be between 0 and 3600")
	}
	if input.Schedule.MaxRuntimeSeconds < 60 || input.Schedule.MaxRuntimeSeconds > 7*24*60*60 {
		return validationError("schedule.max_runtime_seconds", "must be between 60 seconds and 7 days")
	}
	if input.Schedule.GraceSeconds < 60 || input.Schedule.GraceSeconds > 7*24*60*60 {
		return validationError("schedule.grace_seconds", "must be between 60 seconds and 7 days")
	}
	if input.Schedule.MissedRunPolicy != "skip" {
		return validationError("schedule.missed_run_policy", "the current version supports only skip")
	}
	if input.Schedule.ConcurrencyPolicy != "forbid" {
		return validationError("schedule.concurrency_policy", "the current version supports only forbid")
	}
	if err := schedule.Validate(input.Schedule.Cron, input.Schedule.Timezone); err != nil {
		return validationError("schedule", err.Error())
	}
	if err := validateProjectPolicy(&input.Policy); err != nil {
		return err
	}
	return validateMaintenancePolicy(&input.Policy)
}

func (s *Service) SetProjectEnabled(ctx context.Context, projectID string, enabled bool) (domain.Project, error) {
	if strings.TrimSpace(projectID) == "" {
		return domain.Project{}, validationError("project_id", "is required")
	}
	project, err := s.store.SetProjectEnabled(ctx, projectID, enabled, s.now())
	if err != nil {
		return domain.Project{}, err
	}
	return publicProject(project, s.now()), nil
}

// ArchiveServer retires a server that is no longer backed up. The server is
// hidden from the console, its agent credentials are revoked, and its offline
// incidents are closed. Historical runs and audit events keep referencing it.
func (s *Service) ArchiveServer(ctx context.Context, serverID string) (domain.Server, error) {
	serverID = strings.TrimSpace(serverID)
	if serverID == "" {
		return domain.Server{}, validationError("server_id", "is required")
	}
	projects, err := s.store.ListProjects(ctx)
	if err != nil {
		return domain.Server{}, err
	}
	for _, project := range projects {
		if project.ServerID == serverID {
			return domain.Server{}, validationError("server_id", "archive its backup projects first")
		}
	}
	server, err := s.store.ArchiveServer(ctx, serverID, s.now())
	if err != nil {
		return domain.Server{}, err
	}
	_ = s.reconcileAlert(ctx, "agent:"+serverID, nil)
	_ = s.reconcileAlert(ctx, "config:"+serverID, nil)
	return server, nil
}

// ArchiveProject removes a project from scheduling and agent delivery while
// keeping its run history, snapshot index, and audit trail for evidence.
func (s *Service) ArchiveProject(ctx context.Context, projectID string) (domain.Project, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return domain.Project{}, validationError("project_id", "is required")
	}
	project, err := s.store.ArchiveProject(ctx, projectID, s.now())
	if err != nil {
		return domain.Project{}, err
	}
	_ = s.reconcileAlert(ctx, "rpo:"+projectID, nil)
	_ = s.reconcileAlert(ctx, "backup:"+projectID, nil)
	return publicProject(project, s.now()), nil
}

// ArchiveRepository hides an unused storage channel. Repositories referenced
// by active projects are rejected to protect the recovery chain.
func (s *Service) ArchiveRepository(ctx context.Context, repositoryID string) (domain.Repository, error) {
	repositoryID = strings.TrimSpace(repositoryID)
	if repositoryID == "" {
		return domain.Repository{}, validationError("repository_id", "is required")
	}
	projects, err := s.store.ListProjects(ctx)
	if err != nil {
		return domain.Repository{}, err
	}
	for _, project := range projects {
		if project.RepositoryID == repositoryID {
			return domain.Repository{}, validationError("repository_id", "is still used by active backup projects; archive them first")
		}
	}
	return s.store.ArchiveRepository(ctx, repositoryID, s.now())
}

func (s *Service) ProjectHealth(ctx context.Context) ([]domain.ProjectHealth, error) {
	projects, err := s.store.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	activityItems, err := s.store.ListProjectBackupActivity(ctx)
	if err != nil {
		return nil, err
	}
	activity := make(map[string]domain.ProjectBackupActivity, len(activityItems))
	for _, item := range activityItems {
		activity[item.ProjectID] = item
	}
	now := s.now()
	result := make([]domain.ProjectHealth, 0, len(projects))
	for _, project := range projects {
		item := activity[project.ID]
		health := domain.ProjectHealth{
			ProjectID:        project.ID,
			LatestRunStatus:  item.LatestRunStatus,
			LatestRunAt:      item.LatestRunAt,
			LastSuccessfulAt: item.LastSuccessfulAt,
		}
		if !project.Enabled {
			health.Status = "paused"
			result = append(result, health)
			continue
		}
		location, locationErr := time.LoadLocation(project.Schedule.Timezone)
		parsed, scheduleErr := schedule.Parser.Parse(project.Schedule.Cron)
		if locationErr != nil || scheduleErr != nil {
			health.Status = "invalid"
			result = append(result, health)
			continue
		}
		reference := project.CreatedAt
		if item.LastSuccessfulAt != nil {
			reference = *item.LastSuccessfulAt
		}
		expected := parsed.Next(reference.In(location)).UTC()
		graceSeconds := project.Schedule.GraceSeconds
		if graceSeconds <= 0 {
			graceSeconds = int(defaultScheduleGrace.Seconds())
		}
		maxRuntimeSeconds := project.Schedule.MaxRuntimeSeconds
		if maxRuntimeSeconds <= 0 {
			maxRuntimeSeconds = 6 * 60 * 60
		}
		deadline := expected.Add(time.Duration(project.Schedule.JitterSeconds+maxRuntimeSeconds+graceSeconds) * time.Second)
		health.ExpectedAt = &expected
		health.DeadlineAt = &deadline
		// A backup that started for this slot and is still inside its execution
		// window is reported as running. Past the deadline the run report can no
		// longer be trusted, so health escalates back to late/overdue.
		running := now.Before(deadline) && item.LatestRunStatus == domain.RunRunning &&
			item.LatestRunAt != nil && !item.LatestRunAt.Before(expected)
		switch {
		case now.Before(expected):
			if item.LastSuccessfulAt == nil {
				health.Status = "pending"
			} else {
				health.Status = "healthy"
			}
		case running:
			health.Status = "running"
		case now.Before(deadline):
			health.Status = "late"
		default:
			health.Status = "overdue"
		}
		result = append(result, health)
	}
	return result, nil
}

func (s *Service) Dashboard(ctx context.Context, since time.Time) (domain.Dashboard, error) {
	dashboard, err := s.store.Dashboard(ctx, since)
	if err != nil {
		return domain.Dashboard{}, err
	}
	health, err := s.ProjectHealth(ctx)
	if err != nil {
		return domain.Dashboard{}, err
	}
	for _, item := range health {
		switch item.Status {
		case "late":
			dashboard.ProjectsLate++
		case "overdue":
			dashboard.ProjectsOverdue++
		}
	}
	return dashboard, nil
}

func (s *Service) DesiredConfig(ctx context.Context, serverID string) (domain.AgentConfig, error) {
	config, err := s.store.DesiredConfig(ctx, serverID)
	if err != nil {
		return domain.AgentConfig{}, err
	}
	degraded := make([]domain.DegradedProject, 0)
	projects := make([]domain.AgentProject, 0, len(config.Projects))
	for _, project := range config.Projects {
		if reason := s.decryptProjectSecrets(&project); reason != "" {
			degraded = append(degraded, domain.DegradedProject{
				ProjectID: project.ID, ProjectName: project.Name, Reason: reason,
			})
			continue
		}
		projects = append(projects, project)
	}
	config.Projects = projects
	config.DegradedProjects = degraded
	// Surface a server-scoped alert so credential loss is visible even when
	// the Agent itself never reports a failure. Best-effort: a notification
	// failure must not block the configuration delivery.
	_ = s.reconcileConfigDegradation(ctx, serverID, config.Revision, degraded)
	return config, nil
}

// decryptProjectSecrets unseals the repository credentials and database
// passwords for one project. It returns a human-readable reason when the
// project must be dropped from the agent configuration.
func (s *Service) decryptProjectSecrets(project *domain.AgentProject) string {
	repository := &project.Repository
	plaintext, err := s.sealer.Open(repository.SecretCiphertext)
	if err != nil {
		return "repository credentials cannot be decrypted: " + err.Error()
	}
	var payload struct {
		Password    string            `json:"password"`
		Environment map[string]string `json:"environment"`
		Options     map[string]string `json:"options,omitempty"`
	}
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return "repository credentials cannot be decoded: " + err.Error()
	}
	repository.Password = payload.Password
	repository.Environment = payload.Environment
	repository.Options = payload.Options
	repository.SecretCiphertext = nil
	repository.URL = strings.TrimRight(repository.URL, "/") + "/" + project.ServerID
	for sourceIndex := range project.Sources {
		source := &project.Sources[sourceIndex]
		if source.SecretCiphertext == "" {
			continue
		}
		password, err := s.sealer.Open([]byte(source.SecretCiphertext))
		if err != nil {
			return "database credentials cannot be decrypted: " + err.Error()
		}
		if source.Database == nil {
			return "source has a secret but no database configuration"
		}
		source.Database.Password = string(password)
		source.SecretCiphertext = ""
	}
	return ""
}

// reconcileConfigDegradation keeps one firing incident per server while any
// project secret cannot be unsealed, and resolves it once decryption recovers.
func (s *Service) reconcileConfigDegradation(ctx context.Context, serverID string, revision int64, degraded []domain.DegradedProject) error {
	if len(degraded) == 0 {
		return s.reconcileAlert(ctx, "config:"+serverID, nil)
	}
	names := make([]string, 0, len(degraded))
	ids := make([]string, 0, len(degraded))
	for _, item := range degraded {
		names = append(names, fmt.Sprintf("%s (%s)", item.ProjectName, item.ProjectID))
		ids = append(ids, item.ProjectID)
	}
	sort.Strings(ids)
	sort.Strings(names)
	condition := &alertCondition{
		Kind: "config_error", ResourceType: "server", ResourceID: serverID,
		SourceEventID: fmt.Sprintf("rev%d:%s", revision, strings.Join(ids, ",")),
		Severity:      "critical",
		Summary:       "Agent 配置降级",
		Description:   "以下项目的仓库或数据源凭据无法解密，已从下发给该 Agent 的配置中移除：" + strings.Join(names, "、") + "。请检查 VAULTMESH_MASTER_KEY 是否与加密时一致。",
	}
	return s.reconcileAlert(ctx, "config:"+serverID, condition)
}

// CreateDetectionCommand queues a read-only inventory scan on one server.
// The report is delivered through the dedicated detection endpoint and
// surfaced to the wizard; it never starts a backup by itself.
func (s *Service) CreateDetectionCommand(ctx context.Context, serverID string) (domain.Command, error) {
	serverID = strings.TrimSpace(serverID)
	if serverID == "" {
		return domain.Command{}, validationError("server_id", "is required")
	}
	id, err := randomValue("cmd", 10)
	if err != nil {
		return domain.Command{}, err
	}
	return s.store.CreateCommand(ctx, domain.Command{
		ID:        id,
		ServerID:  serverID,
		Type:      "detect",
		CreatedAt: s.now(),
	})
}

func (s *Service) SaveDetectionReport(ctx context.Context, serverID, commandID string, report domain.DetectionReport) error {
	serverID = strings.TrimSpace(serverID)
	commandID = strings.TrimSpace(commandID)
	if commandID == "" {
		return validationError("command_id", "is required")
	}
	// An empty report is a valid result ("nothing detectable on this
	// host") — rejecting it would leave the command looping forever.
	now := s.now()
	report.GeneratedAt = now
	report.CommandID = commandID
	return s.store.SaveDetectionReport(ctx, serverID, commandID, report, now)
}

func (s *Service) GetDetectionReport(ctx context.Context, serverID string) (domain.DetectionReport, bool, error) {
	return s.store.GetDetectionReport(ctx, strings.TrimSpace(serverID))
}

// GetLatestDetectionCommand surfaces the dispatch state (attempts, lease)
// so the console can tell "agent has not answered yet" apart from "the
// command was never delivered" — the primary diagnosis for version skew.
func (s *Service) GetLatestDetectionCommand(ctx context.Context, serverID string) (domain.Command, bool, error) {
	return s.store.GetLatestCommand(ctx, strings.TrimSpace(serverID), "detect")
}

func (s *Service) CreateManualRun(ctx context.Context, projectID string) (domain.Command, error) {
	return s.createProjectCommand(ctx, projectID, "backup", nil)
}

func (s *Service) CreateRetentionPreview(ctx context.Context, projectID string) (domain.Command, error) {
	return s.createProjectCommand(ctx, projectID, "retention_preview", nil)
}

func (s *Service) RefreshSnapshots(ctx context.Context, projectID string) (domain.Command, error) {
	return s.createProjectCommand(ctx, projectID, "snapshot_sync", nil)
}

func (s *Service) SetSnapshotProtected(ctx context.Context, projectID, snapshotID string, protected bool) (domain.Command, error) {
	if err := s.snapshotForCommand(ctx, projectID, snapshotID); err != nil {
		return domain.Command{}, err
	}
	return s.createProjectCommand(ctx, projectID, "snapshot_protect", map[string]any{
		"snapshot_id": snapshotID,
		"protected":   protected,
	})
}

func (s *Service) BrowseSnapshot(ctx context.Context, projectID, snapshotID, snapshotPath string) (domain.Command, error) {
	if err := s.snapshotForCommand(ctx, projectID, snapshotID); err != nil {
		return domain.Command{}, err
	}
	cleaned, err := normalizeSnapshotPath(snapshotPath)
	if err != nil {
		return domain.Command{}, err
	}
	return s.createProjectCommand(ctx, projectID, "snapshot_browse", map[string]any{
		"snapshot_id": snapshotID,
		"path":        cleaned,
	})
}

func (s *Service) RestoreSnapshot(ctx context.Context, projectID, snapshotID, snapshotPath string) (domain.Command, error) {
	if err := s.snapshotForCommand(ctx, projectID, snapshotID); err != nil {
		return domain.Command{}, err
	}
	cleaned, err := normalizeSnapshotPath(snapshotPath)
	if err != nil {
		return domain.Command{}, err
	}
	return s.createProjectCommand(ctx, projectID, "snapshot_restore", map[string]any{
		"snapshot_id": snapshotID,
		"path":        cleaned,
	})
}

func (s *Service) snapshotForCommand(ctx context.Context, projectID, snapshotID string) error {
	if !fullResticSnapshotID.MatchString(snapshotID) {
		return validationError("snapshot_id", "must be a full 64-character Restic snapshot ID")
	}
	_, err := s.store.GetSnapshot(ctx, projectID, snapshotID)
	return err
}

func normalizeSnapshotPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = "/"
	}
	if strings.ContainsAny(value, "\x00\r\n") || !strings.HasPrefix(value, "/") {
		return "", validationError("path", "must be an absolute snapshot path")
	}
	cleaned := pathpkg.Clean(value)
	if cleaned == "." || !strings.HasPrefix(cleaned, "/") {
		return "", validationError("path", "must be an absolute snapshot path")
	}
	return cleaned, nil
}

func (s *Service) createProjectCommand(ctx context.Context, projectID, commandType string, payload map[string]any) (domain.Command, error) {
	if strings.TrimSpace(projectID) == "" {
		return domain.Command{}, validationError("project_id", "is required")
	}
	id, err := randomValue("cmd", 10)
	if err != nil {
		return domain.Command{}, err
	}
	return s.store.CreateCommand(ctx, domain.Command{
		ID:        id,
		ProjectID: projectID,
		Type:      commandType,
		Payload:   payload,
		CreatedAt: s.now(),
	})
}

func (s *Service) ClaimCommands(ctx context.Context, serverID string) ([]domain.Command, error) {
	now := s.now()
	return s.store.ClaimCommands(ctx, serverID, now, now.Add(2*time.Minute), 10)
}

func (s *Service) Store() store.Store { return s.store }

type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }

func validationError(field, message string) error {
	return &ValidationError{Field: field, Message: message}
}

func randomValue(prefix string, size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate random value: %w", err)
	}
	value := strings.TrimRight(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buffer), "=")
	return prefix + "_" + strings.ToLower(value), nil
}

func hashToken(token string) []byte {
	digest := sha256.Sum256([]byte(token))
	return digest[:]
}

func validateSourcePath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || !filepath.IsAbs(value) {
		return "", fmt.Errorf("path %q must be absolute", value)
	}
	cleaned := filepath.Clean(value)
	if cleaned == string(filepath.Separator) {
		return "", errors.New("backing up the filesystem root is not allowed in the current version")
	}
	for _, forbidden := range []string{"/proc", "/sys", "/dev"} {
		if cleaned == forbidden || strings.HasPrefix(cleaned, forbidden+string(filepath.Separator)) {
			return "", fmt.Errorf("path %q is a virtual system path and is not allowed", cleaned)
		}
	}
	// The default agent state directory holds plaintext repository and
	// database credentials; it must never enter a snapshot. The Agent also
	// enforces this for custom state paths the control plane cannot see.
	if cleaned == "/var/lib/vaultmesh-agent" || strings.HasPrefix(cleaned, "/var/lib/vaultmesh-agent/") {
		return "", fmt.Errorf("path %q is the agent state directory and contains plaintext credentials; it cannot be backed up", cleaned)
	}
	return cleaned, nil
}

func publicProject(project domain.Project, now time.Time) domain.Project {
	if project.Schedule.GraceSeconds <= 0 {
		project.Schedule.GraceSeconds = int(defaultScheduleGrace.Seconds())
	}
	if project.Enabled {
		if location, err := time.LoadLocation(project.Schedule.Timezone); err == nil {
			if parsed, err := schedule.Parser.Parse(project.Schedule.Cron); err == nil {
				next := parsed.Next(now.In(location)).UTC()
				project.NextRunAt = &next
			}
		}
	}
	for index := range project.Sources {
		project.Sources[index].SecretCiphertext = ""
		if project.Sources[index].Database != nil {
			database := *project.Sources[index].Database
			database.Password = ""
			project.Sources[index].Database = &database
		}
	}
	return project
}
