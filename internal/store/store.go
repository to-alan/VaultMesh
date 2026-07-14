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
	ListProjects(context.Context) ([]domain.Project, error)
	DesiredConfig(context.Context, string) (domain.AgentConfig, error)
	CreateCommand(context.Context, domain.Command) (domain.Command, error)
	ClaimCommands(context.Context, string, time.Time, time.Time, int) ([]domain.Command, error)

	UpsertRun(context.Context, domain.RunReport) error
	ListRuns(context.Context, int) ([]domain.RunReport, error)
	Dashboard(context.Context, time.Time) (domain.Dashboard, error)
}
