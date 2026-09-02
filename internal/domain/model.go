// Package domain defines the shared data structures and state constants
// used across the control plane, agent, and storage layers.
package domain

import "time"

const (
	ServerPending = "pending"
	ServerOnline  = "online"
	ServerOffline = "offline"

	RunRunning   = "running"
	RunSucceeded = "succeeded"
	RunPartial   = "partial"
	RunFailed    = "failed"
	RunCanceled  = "canceled"
	RunTimedOut  = "timed_out"
	RunUnknown   = "unknown"
	RunSkipped   = "skipped"

	AuditSucceeded = "succeeded"
	AuditFailed    = "failed"

	AgentOfflineAfter = 90 * time.Second
)

type Server struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Hostname        string     `json:"hostname,omitempty"`
	OS              string     `json:"os,omitempty"`
	Arch            string     `json:"arch,omitempty"`
	AgentVersion    string     `json:"agent_version,omitempty"`
	Status          string     `json:"status"`
	LastSeenAt      *time.Time `json:"last_seen_at,omitempty"`
	DesiredRevision int64      `json:"desired_revision"`
	AppliedRevision int64      `json:"applied_revision"`
	ArchivedAt      *time.Time `json:"archived_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
}

type Repository struct {
	ID               string            `json:"id"`
	Provider         string            `json:"provider"`
	Name             string            `json:"name"`
	URL              string            `json:"url"`
	Password         string            `json:"password,omitempty"`
	Environment      map[string]string `json:"environment,omitempty"`
	Options          map[string]string `json:"options,omitempty"`
	SecretCiphertext []byte            `json:"-"`
	ArchivedAt       *time.Time        `json:"archived_at,omitempty"`
	CreatedAt        time.Time         `json:"created_at"`
}

type Source struct {
	ID               string          `json:"id"`
	Type             string          `json:"type"`
	Paths            []string        `json:"paths,omitempty"`
	Excludes         []string        `json:"excludes,omitempty"`
	Database         *DatabaseSource `json:"database,omitempty"`
	Docker           *DockerSource   `json:"docker,omitempty"`
	SecretCiphertext string          `json:"secret_ciphertext,omitempty"`
	Required         bool            `json:"required"`
}

type DockerSource struct {
	Containers     []string `json:"containers"`
	IncludeVolumes bool     `json:"include_volumes"`
}

type DatabaseSource struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password,omitempty"`
	Database string `json:"database"`
}

type Schedule struct {
	Cron              string `json:"cron"`
	Timezone          string `json:"timezone"`
	JitterSeconds     int    `json:"jitter_seconds"`
	MaxRuntimeSeconds int    `json:"max_runtime_seconds"`
	GraceSeconds      int    `json:"grace_seconds"`
	MissedRunPolicy   string `json:"missed_run_policy"`
	ConcurrencyPolicy string `json:"concurrency_policy"`
}

// ProjectPolicy maps the project-level controls to Restic's native backup,
// forget/prune, and check commands. Keeping this separate from Schedule makes
// the execution policy portable when additional schedulers are introduced.
type ProjectPolicy struct {
	Backup       BackupPolicy       `json:"backup"`
	Retention    RetentionPolicy    `json:"retention"`
	Verification VerificationPolicy `json:"verification"`
	Maintenance  MaintenancePolicy  `json:"maintenance"`
}

type MaintenancePolicy struct {
	// Separate keeps repository maintenance out of the backup critical path.
	// It is explicit so projects created before this capability retain their
	// original post-backup behavior until they are edited.
	Separate         bool   `json:"separate"`
	Timezone         string `json:"timezone,omitempty"`
	RetentionCron    string `json:"retention_cron,omitempty"`
	PruneCron        string `json:"prune_cron,omitempty"`
	VerificationCron string `json:"verification_cron,omitempty"`
}

type BackupPolicy struct {
	OneFileSystem     bool     `json:"one_file_system"`
	ExcludeCaches     bool     `json:"exclude_caches"`
	ExcludeIfPresent  []string `json:"exclude_if_present,omitempty"`
	ExcludeLargerThan string   `json:"exclude_larger_than,omitempty"`
}

type RetentionPolicy struct {
	Enabled bool `json:"enabled"`
	// Mode is one of count, smart, gfs, or age. Empty values from older
	// configurations are normalized to gfs by the control plane.
	Mode        string `json:"mode"`
	KeepLast    int    `json:"keep_last"`
	KeepHourly  int    `json:"keep_hourly"`
	KeepDaily   int    `json:"keep_daily"`
	KeepWeekly  int    `json:"keep_weekly"`
	KeepMonthly int    `json:"keep_monthly"`
	KeepYearly  int    `json:"keep_yearly"`
	KeepWithin  string `json:"keep_within,omitempty"`
	Prune       bool   `json:"prune"`
}

type VerificationPolicy struct {
	// Mode is one of off, metadata, subset, or full.
	Mode           string `json:"mode"`
	ReadDataSubset string `json:"read_data_subset,omitempty"`
}

type Project struct {
	ID           string        `json:"id"`
	ServerID     string        `json:"server_id"`
	RepositoryID string        `json:"repository_id"`
	Name         string        `json:"name"`
	Enabled      bool          `json:"enabled"`
	Sources      []Source      `json:"sources"`
	Schedule     Schedule      `json:"schedule"`
	Policy       ProjectPolicy `json:"policy"`
	Revision     int64         `json:"revision"`
	NextRunAt    *time.Time    `json:"next_run_at,omitempty"`
	ArchivedAt   *time.Time    `json:"archived_at,omitempty"`
	CreatedAt    time.Time     `json:"created_at"`
	UpdatedAt    time.Time     `json:"updated_at"`
}

type AgentProject struct {
	Project
	Repository Repository `json:"repository"`
}

// RetentionScope selects which control-plane table a PruneBefore call
// removes expired rows from.
type RetentionScope string

const (
	RetentionRuns       RetentionScope = "runs"
	RetentionCommands   RetentionScope = "commands"
	RetentionDeliveries RetentionScope = "notification_deliveries"
	RetentionIncidents  RetentionScope = "alert_incidents"
	RetentionAudit      RetentionScope = "audit_events"
)

// DataRetention controls per-scope pruning of finished control-plane facts.
// A scope with zero or negative days is never pruned.
type DataRetention struct {
	RunsDays        int
	CommandsDays    int
	DeliveriesDays  int
	IncidentsDays   int
	AuditEventsDays int
}

// Enabled reports whether any retention scope will prune rows.
func (r DataRetention) Enabled() bool {
	return r.RunsDays > 0 || r.CommandsDays > 0 || r.DeliveriesDays > 0 ||
		r.IncidentsDays > 0 || r.AuditEventsDays > 0
}

// DegradedProject describes a project that was dropped from an agent
// configuration because its repository or database secrets cannot be
// unsealed with the current master key.
type DegradedProject struct {
	ProjectID   string `json:"project_id"`
	ProjectName string `json:"project_name"`
	Reason      string `json:"reason"`
}

type AgentConfig struct {
	Revision int64          `json:"revision"`
	Projects []AgentProject `json:"projects"`
	// DegradedProjects lists projects that were omitted because their secrets
	// could not be decrypted. Non-empty only on master-key incidents.
	DegradedProjects []DegradedProject `json:"degraded_projects,omitempty"`
}

type EnrollmentResult struct {
	Server          Server    `json:"server"`
	EnrollmentToken string    `json:"enrollment_token"`
	ExpiresAt       time.Time `json:"expires_at"`
}

type AgentIdentity struct {
	AgentID string `json:"agent_id"`
	Token   string `json:"token"`
}

type AgentInfo struct {
	Hostname     string `json:"hostname"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	AgentVersion string `json:"agent_version"`
}

type Heartbeat struct {
	AgentInfo
	AppliedRevision int64 `json:"applied_revision"`
}

type RunReport struct {
	ID             string         `json:"id"`
	IdempotencyKey string         `json:"idempotency_key"`
	ProjectID      string         `json:"project_id"`
	ServerID       string         `json:"server_id,omitempty"`
	ScheduledAt    time.Time      `json:"scheduled_at"`
	StartedAt      time.Time      `json:"started_at"`
	FinishedAt     *time.Time     `json:"finished_at,omitempty"`
	Status         string         `json:"status"`
	SnapshotID     string         `json:"snapshot_id,omitempty"`
	ErrorCode      string         `json:"error_code,omitempty"`
	ErrorMessage   string         `json:"error_message,omitempty"`
	Stats          map[string]any `json:"stats,omitempty"`
}

type Snapshot struct {
	ID           string    `json:"id"`
	ProjectID    string    `json:"project_id"`
	ServerID     string    `json:"server_id"`
	Time         time.Time `json:"time"`
	Hostname     string    `json:"hostname"`
	Username     string    `json:"username,omitempty"`
	Paths        []string  `json:"paths"`
	Tags         []string  `json:"tags"`
	TotalFiles   int64     `json:"total_files,omitempty"`
	TotalBytes   int64     `json:"total_bytes,omitempty"`
	Protected    bool      `json:"protected"`
	LastSyncedAt time.Time `json:"last_synced_at"`
}

type SnapshotEntry struct {
	Name        string    `json:"name"`
	Path        string    `json:"path"`
	Type        string    `json:"type"`
	Size        int64     `json:"size"`
	Mode        uint32    `json:"mode,omitempty"`
	Permissions string    `json:"permissions,omitempty"`
	ModifiedAt  time.Time `json:"modified_at,omitempty"`
}

type Dashboard struct {
	ServersTotal    int `json:"servers_total"`
	ServersOnline   int `json:"servers_online"`
	ProjectsTotal   int `json:"projects_total"`
	RunsSucceeded   int `json:"runs_succeeded"`
	RunsFailed      int `json:"runs_failed"`
	RunsPartial     int `json:"runs_partial"`
	ProjectsLate    int `json:"projects_late"`
	ProjectsOverdue int `json:"projects_overdue"`
}

// ProjectBackupActivity contains the minimum run history needed to derive
// schedule health without exposing or rescanning arbitrary run payloads.
type ProjectBackupActivity struct {
	ProjectID        string     `json:"project_id"`
	LatestRunID      string     `json:"latest_run_id,omitempty"`
	LatestRunStatus  string     `json:"latest_run_status,omitempty"`
	LatestRunAt      *time.Time `json:"latest_run_at,omitempty"`
	LastSuccessfulAt *time.Time `json:"last_successful_at,omitempty"`
}

// NotificationChannel is a user-defined contact point. Config is accepted on
// writes only; SecretCiphertext is the encrypted-at-rest representation and is
// never serialized by the management API.
type NotificationChannel struct {
	ID                    string            `json:"id"`
	Name                  string            `json:"name"`
	Type                  string            `json:"type"`
	Enabled               bool              `json:"enabled"`
	SendResolved          bool              `json:"send_resolved"`
	RepeatIntervalSeconds int               `json:"repeat_interval_seconds"`
	EventTypes            []string          `json:"event_types"`
	ProjectIDs            []string          `json:"project_ids,omitempty"`
	ServerIDs             []string          `json:"server_ids,omitempty"`
	Config                map[string]string `json:"config,omitempty"`
	Destination           string            `json:"destination,omitempty"`
	Configured            bool              `json:"configured"`
	SecretCiphertext      []byte            `json:"-"`
	CreatedAt             time.Time         `json:"created_at"`
	UpdatedAt             time.Time         `json:"updated_at"`
	DeletedAt             *time.Time        `json:"-"`
}

type AlertIncident struct {
	ID              string     `json:"id"`
	Fingerprint     string     `json:"fingerprint"`
	Kind            string     `json:"kind"`
	ResourceType    string     `json:"resource_type"`
	ResourceID      string     `json:"resource_id"`
	ResourceName    string     `json:"resource_name"`
	ProjectID       string     `json:"project_id,omitempty"`
	ProjectName     string     `json:"project_name,omitempty"`
	Status          string     `json:"status"`
	Severity        string     `json:"severity"`
	Summary         string     `json:"summary"`
	Description     string     `json:"description"`
	SourceEventID   string     `json:"source_event_id"`
	OccurrenceCount int        `json:"occurrence_count"`
	StartedAt       time.Time  `json:"started_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	ResolvedAt      *time.Time `json:"resolved_at,omitempty"`
}

type NotificationDelivery struct {
	ID            string     `json:"id"`
	AlertID       string     `json:"alert_id"`
	ChannelID     string     `json:"channel_id"`
	ChannelName   string     `json:"channel_name,omitempty"`
	Transition    string     `json:"transition"`
	DedupeKey     string     `json:"-"`
	Status        string     `json:"status"`
	AttemptCount  int        `json:"attempt_count"`
	NextAttemptAt time.Time  `json:"next_attempt_at"`
	LeaseUntil    *time.Time `json:"-"`
	LastError     string     `json:"last_error,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	SentAt        *time.Time `json:"sent_at,omitempty"`
}

// ProjectHealth distinguishes a job that is merely inside its execution/grace
// window from one that has actually missed its recovery-point objective.
type ProjectHealth struct {
	ProjectID        string     `json:"project_id"`
	Status           string     `json:"status"`
	LatestRunStatus  string     `json:"latest_run_status,omitempty"`
	LatestRunAt      *time.Time `json:"latest_run_at,omitempty"`
	LastSuccessfulAt *time.Time `json:"last_successful_at,omitempty"`
	ExpectedAt       *time.Time `json:"expected_at,omitempty"`
	DeadlineAt       *time.Time `json:"deadline_at,omitempty"`
}

type Command struct {
	ID        string         `json:"id"`
	ServerID  string         `json:"server_id"`
	ProjectID string         `json:"project_id,omitempty"`
	Type      string         `json:"type"`
	Payload   map[string]any `json:"payload,omitempty"`
	// ProjectID is empty for server-scoped commands such as "detect".
	LeaseUntil *time.Time `json:"lease_until,omitempty"`
	Attempts   int        `json:"attempts"`
	CreatedAt  time.Time  `json:"created_at"`
}

// DetectionDispatch is the create-detection response: the queued command
// plus an optional compatibility warning for agents that predate the
// detect command.
type DetectionDispatch struct {
	Command Command `json:"command"`
	Warning string  `json:"warning,omitempty"`
}

// DetectionReport is a read-only inventory of backup-worthy facts on one
// agent host: running containers, database signals, and application roots
// discovered by marker files. It never contains secrets; the control-plane
// wizard turns selected findings into a project draft for human review.
type DetectionReport struct {
	GeneratedAt time.Time           `json:"generated_at"`
	CommandID   string              `json:"command_id,omitempty"`
	Containers  []DetectedContainer `json:"containers,omitempty"`
	Databases   []DetectedDatabase  `json:"databases,omitempty"`
	Apps        []DetectedApp       `json:"apps,omitempty"`
	Tools       map[string]string   `json:"tools,omitempty"`
}

type DetectedContainer struct {
	Name    string   `json:"name"`
	Image   string   `json:"image"`
	Running bool     `json:"running"`
	Ports   []string `json:"ports,omitempty"`
	Mounts  []string `json:"mounts,omitempty"`
}

type DetectedDatabase struct {
	Kind      string `json:"kind"` // mysql | postgresql
	Source    string `json:"source"`
	Container string `json:"container,omitempty"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Reachable bool   `json:"reachable"`
	DumpTool  string `json:"dump_tool,omitempty"`
}

type DetectedApp struct {
	Path    string   `json:"path"`
	Name    string   `json:"name"`
	Kind    string   `json:"kind"`
	Markers []string `json:"markers"`
}

// AuditEvent is an append-only record of a security-sensitive control-plane
// action. It deliberately contains no request body or arbitrary metadata so
// credentials and source configuration cannot leak into the audit trail.
type AuditEvent struct {
	ID           string    `json:"id"`
	Actor        string    `json:"actor"`
	Action       string    `json:"action"`
	ResourceType string    `json:"resource_type,omitempty"`
	ResourceID   string    `json:"resource_id,omitempty"`
	Outcome      string    `json:"outcome"`
	ClientIP     string    `json:"client_ip"`
	StatusCode   int       `json:"status_code"`
	CreatedAt    time.Time `json:"created_at"`
}

// AdminAccount is the single control-plane administrator identity. SecurityData
// contains an authenticated-encryption envelope managed by the control service.
type AdminAccount struct {
	Username       string    `json:"username"`
	PasswordHash   []byte    `json:"-"`
	WebAuthnUserID []byte    `json:"-"`
	SecurityData   []byte    `json:"-"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}
