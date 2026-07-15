package store

import (
	"context"
	"errors"
	"time"

	"github.com/to-alan/vaultmesh/internal/domain"
)

var (
	ErrNotFound          = errors.New("not found")
	ErrConflict          = errors.New("conflict")
	ErrInvalidEnrollment = errors.New("invalid or expired enrollment")
	ErrUnauthorized      = errors.New("unauthorized")
)

type Store interface {
	Ping(context.Context) error
	Close()

	GetAdminAccount(context.Context) (domain.AdminAccount, error)
	SaveAdminAccount(context.Context, domain.AdminAccount) error

	CreateServer(context.Context, domain.Server, []byte, time.Time) (domain.Server, error)
	EnrollAgent(context.Context, []byte, []byte, domain.AgentInfo) (domain.Server, error)
	AuthenticateAgent(context.Context, []byte) (domain.Server, error)
	UpdateHeartbeat(context.Context, string, domain.Heartbeat, time.Time) error
	ListServers(context.Context) ([]domain.Server, error)

	CreateRepository(context.Context, domain.Repository) (domain.Repository, error)
	ListRepositories(context.Context) ([]domain.Repository, error)
	GetRepository(context.Context, string) (domain.Repository, error)

	CreateProject(context.Context, domain.Project) (domain.Project, error)
	GetProject(context.Context, string) (domain.Project, error)
	ListProjects(context.Context) ([]domain.Project, error)
	UpdateProject(context.Context, domain.Project, time.Time) (domain.Project, error)
	SetProjectEnabled(context.Context, string, bool, time.Time) (domain.Project, error)
	DesiredConfig(context.Context, string) (domain.AgentConfig, error)
	CreateCommand(context.Context, domain.Command) (domain.Command, error)
	ClaimCommands(context.Context, string, time.Time, time.Time, int) ([]domain.Command, error)
	ReplaceProjectSnapshots(context.Context, string, string, []domain.Snapshot, time.Time) error
	ListSnapshots(context.Context, string, int) ([]domain.Snapshot, error)
	GetSnapshot(context.Context, string, string) (domain.Snapshot, error)

	UpsertRun(context.Context, domain.RunReport) error
	ListRuns(context.Context, int) ([]domain.RunReport, error)
	ListProjectBackupActivity(context.Context) ([]domain.ProjectBackupActivity, error)

	CreateNotificationChannel(context.Context, domain.NotificationChannel) (domain.NotificationChannel, error)
	GetNotificationChannel(context.Context, string) (domain.NotificationChannel, error)
	ListNotificationChannels(context.Context) ([]domain.NotificationChannel, error)
	UpdateNotificationChannel(context.Context, domain.NotificationChannel) (domain.NotificationChannel, error)
	ArchiveNotificationChannel(context.Context, string, time.Time) error
	CreateAlertIncident(context.Context, domain.AlertIncident) (domain.AlertIncident, error)
	GetAlertIncident(context.Context, string) (domain.AlertIncident, error)
	GetFiringAlertIncident(context.Context, string) (domain.AlertIncident, error)
	UpdateAlertIncident(context.Context, domain.AlertIncident) (domain.AlertIncident, error)
	ListAlertIncidents(context.Context, int) ([]domain.AlertIncident, error)
	CreateNotificationDelivery(context.Context, domain.NotificationDelivery) error
	ClaimNotificationDeliveries(context.Context, time.Time, time.Time, int) ([]domain.NotificationDelivery, error)
	CompleteNotificationDelivery(context.Context, string, bool, string, time.Time, time.Time) error
	ListNotificationDeliveries(context.Context, int) ([]domain.NotificationDelivery, error)

	AppendAuditEvent(context.Context, domain.AuditEvent) error
	ListAuditEvents(context.Context, int) ([]domain.AuditEvent, error)
	Dashboard(context.Context, time.Time) (domain.Dashboard, error)
}
