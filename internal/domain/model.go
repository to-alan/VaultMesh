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
	CreatedAt    time.Time     `json:"created_at"`
	UpdatedAt    time.Time     `json:"updated_at"`
}

type AgentProject struct {
	Project
	Repository Repository `json:"repository"`
}

type AgentConfig struct {
	Revision int64          `json:"revision"`
	Projects []AgentProject `json:"projects"`
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
	ServersTotal  int `json:"servers_total"`
	ServersOnline int `json:"servers_online"`
	ProjectsTotal int `json:"projects_total"`
	RunsSucceeded int `json:"runs_succeeded"`
	RunsFailed    int `json:"runs_failed"`
	RunsPartial   int `json:"runs_partial"`
}

type Command struct {
	ID         string         `json:"id"`
	ServerID   string         `json:"server_id"`
	ProjectID  string         `json:"project_id"`
	Type       string         `json:"type"`
	Payload    map[string]any `json:"payload,omitempty"`
	LeaseUntil *time.Time     `json:"lease_until,omitempty"`
	Attempts   int            `json:"attempts"`
	CreatedAt  time.Time      `json:"created_at"`
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
