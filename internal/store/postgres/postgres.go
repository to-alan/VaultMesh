package postgres

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/to-alan/vaultmesh/internal/domain"
	"github.com/to-alan/vaultmesh/internal/store"
)

//go:embed migrations/001_initial.sql
var initialMigration string

type Store struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	config.MaxConns = 10
	config.MinConns = 1
	config.MaxConnIdleTime = 5 * time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	result := &Store{pool: pool}
	if err := result.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return result, nil
}

func (s *Store) Migrate(ctx context.Context) error {
	connection, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer connection.Release()
	if _, err := connection.Conn().PgConn().Exec(ctx, initialMigration).ReadAll(); err != nil {
		return fmt.Errorf("run database migration: %w", err)
	}
	return nil
}

func (s *Store) Ping(ctx context.Context) error {
	if err := s.pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}
	return nil
}

func (s *Store) Close() { s.pool.Close() }

func (s *Store) CreateServer(ctx context.Context, server domain.Server, tokenHash []byte, expires time.Time) (domain.Server, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Server{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	_, err = tx.Exec(ctx, `
		INSERT INTO servers (id, name, status, created_at)
		VALUES ($1, $2, $3, $4)`, server.ID, server.Name, domain.ServerPending, server.CreatedAt)
	if err != nil {
		return domain.Server{}, mapError(err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO enrollments (token_hash, server_id, expires_at)
		VALUES ($1, $2, $3)`, tokenHash, server.ID, expires)
	if err != nil {
		return domain.Server{}, mapError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Server{}, err
	}
	server.Status = domain.ServerPending
	return server, nil
}

func (s *Store) EnrollAgent(ctx context.Context, enrollmentHash, credentialHash []byte, info domain.AgentInfo) (domain.Server, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Server{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var serverID string
	var expiresAt time.Time
	var usedAt *time.Time
	err = tx.QueryRow(ctx, `
		SELECT server_id, expires_at, used_at
		FROM enrollments
		WHERE token_hash = $1
		FOR UPDATE`, enrollmentHash).Scan(&serverID, &expiresAt, &usedAt)
	if errors.Is(err, pgx.ErrNoRows) || usedAt != nil || time.Now().After(expiresAt) {
		return domain.Server{}, store.ErrInvalidEnrollment
	}
	if err != nil {
		return domain.Server{}, err
	}
	now := time.Now().UTC()
	if _, err = tx.Exec(ctx, `UPDATE enrollments SET used_at = $2 WHERE token_hash = $1`, enrollmentHash, now); err != nil {
		return domain.Server{}, err
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO agent_credentials (token_hash, server_id, created_at)
		VALUES ($1, $2, $3)`, credentialHash, serverID, now); err != nil {
		return domain.Server{}, mapError(err)
	}
	if _, err = tx.Exec(ctx, `
		UPDATE servers
		SET hostname = $2, os = $3, arch = $4, agent_version = $5,
		    status = $6, last_seen_at = $7
		WHERE id = $1`, serverID, info.Hostname, info.OS, info.Arch, info.AgentVersion, domain.ServerOnline, now); err != nil {
		return domain.Server{}, err
	}
	server, err := getServer(ctx, tx, serverID)
	if err != nil {
		return domain.Server{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Server{}, err
	}
	return server, nil
}

func (s *Store) AuthenticateAgent(ctx context.Context, credentialHash []byte) (domain.Server, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT s.id, s.name, s.hostname, s.os, s.arch, s.agent_version,
		       s.status, s.last_seen_at, s.desired_revision, s.applied_revision, s.created_at
		FROM agent_credentials a
		JOIN servers s ON s.id = a.server_id
		WHERE a.token_hash = $1 AND a.revoked_at IS NULL`, credentialHash)
	server, err := scanServer(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Server{}, store.ErrUnauthorized
	}
	return server, err
}

func (s *Store) UpdateHeartbeat(ctx context.Context, serverID string, heartbeat domain.Heartbeat, at time.Time) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE servers
		SET hostname = $2, os = $3, arch = $4, agent_version = $5,
		    applied_revision = $6, status = $7, last_seen_at = $8
		WHERE id = $1`, serverID, heartbeat.Hostname, heartbeat.OS, heartbeat.Arch,
		heartbeat.AgentVersion, heartbeat.AppliedRevision, domain.ServerOnline, at)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) ListServers(ctx context.Context) ([]domain.Server, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, hostname, os, arch, agent_version,
		       CASE WHEN last_seen_at IS NOT NULL AND last_seen_at < NOW() - INTERVAL '90 seconds'
		            THEN $1 ELSE status END,
		       last_seen_at, desired_revision, applied_revision, created_at
		FROM servers
		ORDER BY created_at`, domain.ServerOffline)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.Server
	for rows.Next() {
		server, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, server)
	}
	return result, rows.Err()
}

func (s *Store) CreateRepository(ctx context.Context, repository domain.Repository) (domain.Repository, error) {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO repositories (id, server_id, name, url, secret_ciphertext, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)`, repository.ID, repository.ServerID,
		repository.Name, repository.URL, repository.SecretCiphertext, repository.CreatedAt)
	if err != nil {
		return domain.Repository{}, mapError(err)
	}
	return publicRepository(repository), nil
}

func (s *Store) ListRepositories(ctx context.Context) ([]domain.Repository, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, server_id, name, url, created_at
		FROM repositories ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.Repository
	for rows.Next() {
		var repository domain.Repository
		if err := rows.Scan(&repository.ID, &repository.ServerID, &repository.Name, &repository.URL, &repository.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, repository)
	}
	return result, rows.Err()
}

func (s *Store) GetRepository(ctx context.Context, id string) (domain.Repository, error) {
	var repository domain.Repository
	err := s.pool.QueryRow(ctx, `
		SELECT id, server_id, name, url, secret_ciphertext, created_at
		FROM repositories WHERE id = $1`, id).Scan(&repository.ID, &repository.ServerID,
		&repository.Name, &repository.URL, &repository.SecretCiphertext, &repository.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Repository{}, store.ErrNotFound
	}
	return repository, err
}

func (s *Store) CreateProject(ctx context.Context, project domain.Project) (domain.Project, error) {
	sources, err := json.Marshal(project.Sources)
	if err != nil {
		return domain.Project{}, err
	}
	schedule, err := json.Marshal(project.Schedule)
	if err != nil {
		return domain.Project{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Project{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var repositoryServerID string
	err = tx.QueryRow(ctx, `SELECT server_id FROM repositories WHERE id = $1`, project.RepositoryID).Scan(&repositoryServerID)
	if errors.Is(err, pgx.ErrNoRows) || repositoryServerID != project.ServerID {
		return domain.Project{}, store.ErrNotFound
	}
	if err != nil {
		return domain.Project{}, err
	}
	err = tx.QueryRow(ctx, `
		UPDATE servers SET desired_revision = desired_revision + 1
		WHERE id = $1 RETURNING desired_revision`, project.ServerID).Scan(&project.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Project{}, store.ErrNotFound
	}
	if err != nil {
		return domain.Project{}, err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO projects
		(id, server_id, repository_id, name, enabled, sources, schedule, revision, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`, project.ID,
		project.ServerID, project.RepositoryID, project.Name, project.Enabled, sources,
		schedule, project.Revision, project.CreatedAt, project.UpdatedAt)
	if err != nil {
		return domain.Project{}, mapError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Project{}, err
	}
	return project, nil
}

func (s *Store) ListProjects(ctx context.Context) ([]domain.Project, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, server_id, repository_id, name, enabled, sources, schedule,
		       revision, created_at, updated_at
		FROM projects ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.Project
	for rows.Next() {
		project, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, project)
	}
	return result, rows.Err()
}

func (s *Store) DesiredConfig(ctx context.Context, serverID string) (domain.AgentConfig, error) {
	var revision int64
	if err := s.pool.QueryRow(ctx, `SELECT desired_revision FROM servers WHERE id = $1`, serverID).Scan(&revision); errors.Is(err, pgx.ErrNoRows) {
		return domain.AgentConfig{}, store.ErrNotFound
	} else if err != nil {
		return domain.AgentConfig{}, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT p.id, p.server_id, p.repository_id, p.name, p.enabled, p.sources,
		       p.schedule, p.revision, p.created_at, p.updated_at,
		       r.id, r.server_id, r.name, r.url, r.secret_ciphertext, r.created_at
		FROM projects p
		JOIN repositories r ON r.id = p.repository_id
		WHERE p.server_id = $1 AND p.enabled = TRUE
		ORDER BY p.id`, serverID)
	if err != nil {
		return domain.AgentConfig{}, err
	}
	defer rows.Close()
	config := domain.AgentConfig{Revision: revision}
	for rows.Next() {
		var item domain.AgentProject
		var sources, schedule []byte
		if err := rows.Scan(&item.ID, &item.ServerID, &item.RepositoryID, &item.Project.Name,
			&item.Enabled, &sources, &schedule, &item.Revision, &item.Project.CreatedAt,
			&item.Project.UpdatedAt, &item.Repository.ID, &item.Repository.ServerID,
			&item.Repository.Name, &item.Repository.URL, &item.Repository.SecretCiphertext,
			&item.Repository.CreatedAt); err != nil {
			return domain.AgentConfig{}, err
		}
		if err := json.Unmarshal(sources, &item.Sources); err != nil {
			return domain.AgentConfig{}, fmt.Errorf("decode project sources: %w", err)
		}
		if err := json.Unmarshal(schedule, &item.Schedule); err != nil {
			return domain.AgentConfig{}, fmt.Errorf("decode project schedule: %w", err)
		}
		config.Projects = append(config.Projects, item)
	}
	return config, rows.Err()
}

func (s *Store) CreateCommand(ctx context.Context, command domain.Command) (domain.Command, error) {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO commands (id, server_id, project_id, type, created_at)
		SELECT $1, server_id, id, $3, $4
		FROM projects
		WHERE id = $2 AND enabled = TRUE
		RETURNING server_id`, command.ID, command.ProjectID, command.Type, command.CreatedAt).Scan(&command.ServerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Command{}, store.ErrNotFound
	}
	if err != nil {
		return domain.Command{}, mapError(err)
	}
	return command, nil
}

func (s *Store) ClaimCommands(ctx context.Context, serverID string, now, leaseUntil time.Time, limit int) ([]domain.Command, error) {
	if limit <= 0 || limit > 20 {
		limit = 10
	}
	rows, err := s.pool.Query(ctx, `
		WITH picked AS (
		  SELECT id
		  FROM commands
		  WHERE server_id = $1
		    AND accepted_at IS NULL
		    AND (leased_until IS NULL OR leased_until <= $2)
		  ORDER BY created_at
		  LIMIT $4
		  FOR UPDATE SKIP LOCKED
		)
		UPDATE commands AS c
		SET leased_until = $3, attempts = c.attempts + 1
		FROM picked
		WHERE c.id = picked.id
		RETURNING c.id, c.server_id, c.project_id, c.type, c.leased_until, c.attempts, c.created_at`,
		serverID, now, leaseUntil, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.Command
	for rows.Next() {
		var command domain.Command
		if err := rows.Scan(&command.ID, &command.ServerID, &command.ProjectID, &command.Type,
			&command.LeaseUntil, &command.Attempts, &command.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, command)
	}
	return result, rows.Err()
}

func (s *Store) UpsertRun(ctx context.Context, report domain.RunReport) error {
	stats, err := json.Marshal(report.Stats)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO runs
		(id, idempotency_key, project_id, server_id, scheduled_at, started_at,
		 finished_at, status, snapshot_id, error_code, error_message, stats, updated_at)
		SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, NOW()
		FROM projects
		WHERE id = $3 AND server_id = $4
		ON CONFLICT (id) DO UPDATE SET
		 finished_at = EXCLUDED.finished_at,
		 status = EXCLUDED.status,
		 snapshot_id = EXCLUDED.snapshot_id,
		 error_code = EXCLUDED.error_code,
		 error_message = EXCLUDED.error_message,
		 stats = EXCLUDED.stats,
		 updated_at = NOW()`, report.ID, report.IdempotencyKey, report.ProjectID, report.ServerID,
		report.ScheduledAt, report.StartedAt, report.FinishedAt, report.Status,
		report.SnapshotID, report.ErrorCode, report.ErrorMessage, stats)
	if err != nil {
		return mapError(err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	if commandID, ok := strings.CutPrefix(report.IdempotencyKey, "manual:"); ok {
		completed := report.Status != domain.RunRunning
		_, err := s.pool.Exec(ctx, `
			UPDATE commands
			SET accepted_at = COALESCE(accepted_at, NOW()),
			    completed_at = CASE WHEN $2 THEN COALESCE(completed_at, NOW()) ELSE completed_at END
			WHERE id = $1 AND server_id = $3`, commandID, completed, report.ServerID)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ListRuns(ctx context.Context, limit int) ([]domain.RunReport, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, idempotency_key, project_id, server_id, scheduled_at, started_at,
		       finished_at, status, snapshot_id, error_code, error_message, stats
		FROM runs ORDER BY started_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.RunReport
	for rows.Next() {
		var report domain.RunReport
		var stats []byte
		if err := rows.Scan(&report.ID, &report.IdempotencyKey, &report.ProjectID,
			&report.ServerID, &report.ScheduledAt, &report.StartedAt, &report.FinishedAt,
			&report.Status, &report.SnapshotID, &report.ErrorCode, &report.ErrorMessage,
			&stats); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(stats, &report.Stats); err != nil {
			return nil, err
		}
		result = append(result, report)
	}
	return result, rows.Err()
}

func (s *Store) Dashboard(ctx context.Context, since time.Time) (domain.Dashboard, error) {
	var dashboard domain.Dashboard
	err := s.pool.QueryRow(ctx, `
		SELECT
		  (SELECT COUNT(*) FROM servers),
		  (SELECT COUNT(*) FROM servers WHERE last_seen_at >= NOW() - INTERVAL '90 seconds'),
		  (SELECT COUNT(*) FROM projects),
		  (SELECT COUNT(*) FROM runs WHERE started_at >= $1 AND status = $2),
		  (SELECT COUNT(*) FROM runs WHERE started_at >= $1 AND status IN ($3, $4, $5)),
		  (SELECT COUNT(*) FROM runs WHERE started_at >= $1 AND status = $6)`,
		since, domain.RunSucceeded, domain.RunFailed, domain.RunTimedOut,
		domain.RunUnknown, domain.RunPartial).Scan(&dashboard.ServersTotal,
		&dashboard.ServersOnline, &dashboard.ProjectsTotal, &dashboard.RunsSucceeded,
		&dashboard.RunsFailed, &dashboard.RunsPartial)
	return dashboard, err
}

type scanner interface {
	Scan(...any) error
}

func scanServer(row scanner) (domain.Server, error) {
	var server domain.Server
	err := row.Scan(&server.ID, &server.Name, &server.Hostname, &server.OS, &server.Arch,
		&server.AgentVersion, &server.Status, &server.LastSeenAt, &server.DesiredRevision,
		&server.AppliedRevision, &server.CreatedAt)
	return server, err
}

func getServer(ctx context.Context, tx pgx.Tx, id string) (domain.Server, error) {
	return scanServer(tx.QueryRow(ctx, `
		SELECT id, name, hostname, os, arch, agent_version, status, last_seen_at,
		       desired_revision, applied_revision, created_at
		FROM servers WHERE id = $1`, id))
}

func scanProject(row scanner) (domain.Project, error) {
	var project domain.Project
	var sources, schedule []byte
	err := row.Scan(&project.ID, &project.ServerID, &project.RepositoryID, &project.Name,
		&project.Enabled, &sources, &schedule, &project.Revision, &project.CreatedAt, &project.UpdatedAt)
	if err != nil {
		return domain.Project{}, err
	}
	if err := json.Unmarshal(sources, &project.Sources); err != nil {
		return domain.Project{}, err
	}
	if err := json.Unmarshal(schedule, &project.Schedule); err != nil {
		return domain.Project{}, err
	}
	return project, nil
}

func publicRepository(repository domain.Repository) domain.Repository {
	repository.SecretCiphertext = nil
	repository.Password = ""
	repository.Environment = nil
	return repository
}

func mapError(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23503":
			return store.ErrNotFound
		case "23505":
			return store.ErrConflict
		}
	}
	return err
}
