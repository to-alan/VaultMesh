package postgres

import (
	"context"
	"embed"
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

//go:embed migrations/*.sql
var migrationFiles embed.FS

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
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("list database migrations: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		migration, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read database migration %s: %w", entry.Name(), err)
		}
		if _, err := connection.Conn().PgConn().Exec(ctx, string(migration)).ReadAll(); err != nil {
			return fmt.Errorf("run database migration %s: %w", entry.Name(), err)
		}
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

func (s *Store) GetAdminAccount(ctx context.Context) (domain.AdminAccount, error) {
	var account domain.AdminAccount
	err := s.pool.QueryRow(ctx, `
		SELECT username, password_hash, webauthn_user_id, security_data, created_at, updated_at
		FROM admin_account WHERE id = 'admin'`).Scan(
		&account.Username, &account.PasswordHash, &account.WebAuthnUserID,
		&account.SecurityData, &account.CreatedAt, &account.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.AdminAccount{}, store.ErrNotFound
	}
	return account, err
}

func (s *Store) SaveAdminAccount(ctx context.Context, account domain.AdminAccount) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO admin_account
			(id, username, password_hash, webauthn_user_id, security_data, created_at, updated_at)
		VALUES ('admin', $1, $2, $3, $4, $5, $6)
		ON CONFLICT (id) DO UPDATE SET
			username = EXCLUDED.username,
			password_hash = EXCLUDED.password_hash,
			webauthn_user_id = EXCLUDED.webauthn_user_id,
			security_data = EXCLUDED.security_data,
			updated_at = EXCLUDED.updated_at`,
		account.Username, account.PasswordHash, account.WebAuthnUserID,
		account.SecurityData, account.CreatedAt, account.UpdatedAt)
	return mapError(err)
}

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
		INSERT INTO repositories (id, provider, name, url, secret_ciphertext, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)`, repository.ID, repository.Provider,
		repository.Name, repository.URL, repository.SecretCiphertext, repository.CreatedAt)
	if err != nil {
		return domain.Repository{}, mapError(err)
	}
	return publicRepository(repository), nil
}

func (s *Store) ListRepositories(ctx context.Context) ([]domain.Repository, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, provider, name, url, created_at
		FROM repositories ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.Repository
	for rows.Next() {
		var repository domain.Repository
		if err := rows.Scan(&repository.ID, &repository.Provider, &repository.Name, &repository.URL, &repository.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, repository)
	}
	return result, rows.Err()
}

func (s *Store) GetRepository(ctx context.Context, id string) (domain.Repository, error) {
	var repository domain.Repository
	err := s.pool.QueryRow(ctx, `
		SELECT id, provider, name, url, secret_ciphertext, created_at
		FROM repositories WHERE id = $1`, id).Scan(&repository.ID, &repository.Provider,
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
	policy, err := json.Marshal(project.Policy)
	if err != nil {
		return domain.Project{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Project{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var repositoryID string
	err = tx.QueryRow(ctx, `SELECT id FROM repositories WHERE id = $1`, project.RepositoryID).Scan(&repositoryID)
	if errors.Is(err, pgx.ErrNoRows) {
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
		(id, server_id, repository_id, name, enabled, sources, schedule, policy, revision, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`, project.ID,
		project.ServerID, project.RepositoryID, project.Name, project.Enabled, sources,
		schedule, policy, project.Revision, project.CreatedAt, project.UpdatedAt)
	if err != nil {
		return domain.Project{}, mapError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Project{}, err
	}
	return project, nil
}

func (s *Store) GetProject(ctx context.Context, id string) (domain.Project, error) {
	project, err := scanProject(s.pool.QueryRow(ctx, `
		SELECT id, server_id, repository_id, name, enabled, sources, schedule, policy,
		       revision, created_at, updated_at
		FROM projects WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Project{}, store.ErrNotFound
	}
	return project, err
}

func (s *Store) ListProjects(ctx context.Context) ([]domain.Project, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, server_id, repository_id, name, enabled, sources, schedule, policy,
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

func (s *Store) UpdateProject(ctx context.Context, project domain.Project, updatedAt time.Time) (domain.Project, error) {
	sources, err := json.Marshal(project.Sources)
	if err != nil {
		return domain.Project{}, err
	}
	schedule, err := json.Marshal(project.Schedule)
	if err != nil {
		return domain.Project{}, err
	}
	policy, err := json.Marshal(project.Policy)
	if err != nil {
		return domain.Project{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Project{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	current, err := scanProject(tx.QueryRow(ctx, `
		SELECT id, server_id, repository_id, name, enabled, sources, schedule, policy,
		       revision, created_at, updated_at
		FROM projects WHERE id = $1 FOR UPDATE`, project.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Project{}, store.ErrNotFound
	}
	if err != nil {
		return domain.Project{}, err
	}
	if project.ServerID != current.ServerID || project.RepositoryID != current.RepositoryID {
		return domain.Project{}, store.ErrConflict
	}
	if err := tx.QueryRow(ctx, `
		UPDATE servers SET desired_revision = desired_revision + 1
		WHERE id = $1 RETURNING desired_revision`, current.ServerID).Scan(&project.Revision); err != nil {
		return domain.Project{}, err
	}
	project.Enabled = current.Enabled
	project.CreatedAt = current.CreatedAt
	project.UpdatedAt = updatedAt
	updated, err := scanProject(tx.QueryRow(ctx, `
		UPDATE projects
		SET name = $2, sources = $3, schedule = $4, policy = $5,
		    revision = $6, updated_at = $7
		WHERE id = $1
		RETURNING id, server_id, repository_id, name, enabled, sources, schedule, policy,
		          revision, created_at, updated_at`, project.ID, project.Name, sources,
		schedule, policy, project.Revision, project.UpdatedAt))
	if err != nil {
		return domain.Project{}, mapError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Project{}, err
	}
	return updated, nil
}

func (s *Store) SetProjectEnabled(ctx context.Context, id string, enabled bool, updatedAt time.Time) (domain.Project, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Project{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var serverID string
	var current bool
	if err := tx.QueryRow(ctx, `SELECT server_id, enabled FROM projects WHERE id = $1 FOR UPDATE`, id).Scan(&serverID, &current); errors.Is(err, pgx.ErrNoRows) {
		return domain.Project{}, store.ErrNotFound
	} else if err != nil {
		return domain.Project{}, err
	}
	if current == enabled {
		project, err := scanProject(tx.QueryRow(ctx, `
			SELECT id, server_id, repository_id, name, enabled, sources, schedule, policy,
			       revision, created_at, updated_at FROM projects WHERE id = $1`, id))
		if err != nil {
			return domain.Project{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.Project{}, err
		}
		return project, nil
	}
	var revision int64
	if err := tx.QueryRow(ctx, `
		UPDATE servers SET desired_revision = desired_revision + 1
		WHERE id = $1 RETURNING desired_revision`, serverID).Scan(&revision); err != nil {
		return domain.Project{}, err
	}
	project, err := scanProject(tx.QueryRow(ctx, `
		UPDATE projects SET enabled = $2, revision = $3, updated_at = $4
		WHERE id = $1
		RETURNING id, server_id, repository_id, name, enabled, sources, schedule, policy,
		          revision, created_at, updated_at`, id, enabled, revision, updatedAt))
	if err != nil {
		return domain.Project{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Project{}, err
	}
	return project, nil
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
		       p.schedule, p.policy, p.revision, p.created_at, p.updated_at,
		       r.id, r.provider, r.name, r.url, r.secret_ciphertext, r.created_at
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
		var sources, schedule, policy []byte
		if err := rows.Scan(&item.ID, &item.ServerID, &item.RepositoryID, &item.Project.Name,
			&item.Enabled, &sources, &schedule, &policy, &item.Revision, &item.Project.CreatedAt,
			&item.Project.UpdatedAt, &item.Repository.ID, &item.Repository.Provider,
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
		if err := json.Unmarshal(policy, &item.Policy); err != nil {
			return domain.AgentConfig{}, fmt.Errorf("decode project policy: %w", err)
		}
		config.Projects = append(config.Projects, item)
	}
	return config, rows.Err()
}

func (s *Store) CreateCommand(ctx context.Context, command domain.Command) (domain.Command, error) {
	payload, err := json.Marshal(command.Payload)
	if err != nil {
		return domain.Command{}, err
	}
	err = s.pool.QueryRow(ctx, `
		INSERT INTO commands (id, server_id, project_id, type, payload, created_at)
		SELECT $1, server_id, id, $3, $4, $5
		FROM projects
		WHERE id = $2 AND enabled = TRUE
		RETURNING server_id`, command.ID, command.ProjectID, command.Type, payload, command.CreatedAt).Scan(&command.ServerID)
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
		RETURNING c.id, c.server_id, c.project_id, c.type, c.payload, c.leased_until, c.attempts, c.created_at`,
		serverID, now, leaseUntil, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.Command
	for rows.Next() {
		var command domain.Command
		var payload []byte
		if err := rows.Scan(&command.ID, &command.ServerID, &command.ProjectID, &command.Type,
			&payload, &command.LeaseUntil, &command.Attempts, &command.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(payload, &command.Payload); err != nil {
			return nil, err
		}
		result = append(result, command)
	}
	return result, rows.Err()
}

func (s *Store) ReplaceProjectSnapshots(ctx context.Context, projectID, serverID string, snapshots []domain.Snapshot, syncedAt time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var storedServerID string
	var latestSync *time.Time
	if err := tx.QueryRow(ctx, `SELECT server_id, snapshot_synced_at FROM projects WHERE id = $1 FOR UPDATE`, projectID).Scan(&storedServerID, &latestSync); errors.Is(err, pgx.ErrNoRows) {
		return store.ErrNotFound
	} else if err != nil {
		return err
	}
	if storedServerID != serverID {
		return store.ErrNotFound
	}
	if latestSync != nil && !syncedAt.After(*latestSync) {
		return tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM snapshots WHERE project_id = $1`, projectID); err != nil {
		return err
	}
	for _, snapshot := range snapshots {
		paths, err := json.Marshal(snapshot.Paths)
		if err != nil {
			return err
		}
		tags, err := json.Marshal(snapshot.Tags)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO snapshots
			(id, project_id, server_id, captured_at, hostname, username, paths, tags,
			 total_files, total_bytes, protected, last_synced_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
			snapshot.ID, projectID, serverID, snapshot.Time, snapshot.Hostname, snapshot.Username,
			paths, tags, snapshot.TotalFiles, snapshot.TotalBytes, snapshot.Protected, syncedAt); err != nil {
			return mapError(err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE projects SET snapshot_synced_at = $2 WHERE id = $1`, projectID, syncedAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) ListSnapshots(ctx context.Context, projectID string, limit int) ([]domain.Snapshot, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	query := `SELECT id, project_id, server_id, captured_at, hostname, username, paths, tags,
	                 total_files, total_bytes, protected, last_synced_at
	          FROM snapshots`
	args := []any{}
	if projectID != "" {
		query += ` WHERE project_id = $1`
		args = append(args, projectID)
	}
	query += fmt.Sprintf(` ORDER BY captured_at DESC LIMIT $%d`, len(args)+1)
	args = append(args, limit)
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.Snapshot
	for rows.Next() {
		snapshot, err := scanSnapshot(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, snapshot)
	}
	return result, rows.Err()
}

func (s *Store) GetSnapshot(ctx context.Context, projectID, snapshotID string) (domain.Snapshot, error) {
	snapshot, err := scanSnapshot(s.pool.QueryRow(ctx, `
		SELECT id, project_id, server_id, captured_at, hostname, username, paths, tags,
		       total_files, total_bytes, protected, last_synced_at
		FROM snapshots WHERE project_id = $1 AND id = $2`, projectID, snapshotID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Snapshot{}, store.ErrNotFound
	}
	return snapshot, err
}

func (s *Store) UpsertRun(ctx context.Context, report domain.RunReport) error {
	stats, err := json.Marshal(report.Stats)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `
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
		 updated_at = NOW()
		 WHERE runs.idempotency_key = EXCLUDED.idempotency_key
		   AND runs.project_id = EXCLUDED.project_id
		   AND runs.server_id = EXCLUDED.server_id
		   AND runs.status = 'running'`, report.ID, report.IdempotencyKey, report.ProjectID, report.ServerID,
		report.ScheduledAt, report.StartedAt, report.FinishedAt, report.Status,
		report.SnapshotID, report.ErrorCode, report.ErrorMessage, stats)
	if err != nil {
		return mapError(err)
	}
	if tag.RowsAffected() == 0 {
		var idempotencyKey, projectID, serverID, status string
		err := tx.QueryRow(ctx, `
			SELECT idempotency_key, project_id, server_id, status
			FROM runs WHERE id = $1`, report.ID).Scan(&idempotencyKey, &projectID, &serverID, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return store.ErrNotFound
		}
		if err != nil {
			return err
		}
		if idempotencyKey != report.IdempotencyKey || projectID != report.ProjectID || serverID != report.ServerID {
			return store.ErrConflict
		}
		// The identity matches and the WHERE clause can only reject an already
		// terminal run. Keep processing the related command so a retry can repair
		// a transaction that was interrupted before both facts committed.
	}
	if commandID, ok := strings.CutPrefix(report.IdempotencyKey, "manual:"); ok {
		completed := report.Status != domain.RunRunning
		_, err := tx.Exec(ctx, `
			UPDATE commands
			SET accepted_at = COALESCE(accepted_at, NOW()),
			    completed_at = CASE WHEN $2 THEN COALESCE(completed_at, NOW()) ELSE completed_at END
			WHERE id = $1 AND server_id = $3`, commandID, completed, report.ServerID)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
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

func (s *Store) ListProjectBackupActivity(ctx context.Context) ([]domain.ProjectBackupActivity, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT project.id,
		       COALESCE(latest.id, ''), COALESCE(latest.status, ''), latest.started_at,
		       successful.finished_at
		FROM projects AS project
		LEFT JOIN LATERAL (
			SELECT id, status, started_at
			FROM runs
			WHERE project_id = project.id
			  AND COALESCE(NULLIF(stats->>'operation', ''), 'backup') = 'backup'
			ORDER BY started_at DESC
			LIMIT 1
		) AS latest ON TRUE
		LEFT JOIN LATERAL (
			SELECT COALESCE(finished_at, started_at) AS finished_at
			FROM runs
			WHERE project_id = project.id
			  AND status = 'succeeded'
			  AND COALESCE(NULLIF(stats->>'operation', ''), 'backup') = 'backup'
			ORDER BY COALESCE(finished_at, started_at) DESC
			LIMIT 1
		) AS successful ON TRUE
		ORDER BY project.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.ProjectBackupActivity
	for rows.Next() {
		var item domain.ProjectBackupActivity
		if err := rows.Scan(&item.ProjectID, &item.LatestRunID, &item.LatestRunStatus, &item.LatestRunAt, &item.LastSuccessfulAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) CreateNotificationChannel(ctx context.Context, channel domain.NotificationChannel) (domain.NotificationChannel, error) {
	eventTypes, _ := json.Marshal(channel.EventTypes)
	projectIDs, _ := json.Marshal(channel.ProjectIDs)
	created, err := scanNotificationChannel(s.pool.QueryRow(ctx, `
		INSERT INTO notification_channels
			(id, name, type, enabled, send_resolved, repeat_interval_seconds, event_types, project_ids,
			 secret_ciphertext, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		RETURNING id, name, type, enabled, send_resolved, repeat_interval_seconds, event_types,
		          project_ids, secret_ciphertext, created_at, updated_at, deleted_at`,
		channel.ID, channel.Name, channel.Type, channel.Enabled, channel.SendResolved,
		channel.RepeatIntervalSeconds, eventTypes, projectIDs, channel.SecretCiphertext,
		channel.CreatedAt, channel.UpdatedAt))
	return created, mapError(err)
}

func (s *Store) GetNotificationChannel(ctx context.Context, id string) (domain.NotificationChannel, error) {
	channel, err := scanNotificationChannel(s.pool.QueryRow(ctx, `
		SELECT id, name, type, enabled, send_resolved, repeat_interval_seconds, event_types,
		       project_ids, secret_ciphertext, created_at, updated_at, deleted_at
		FROM notification_channels WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.NotificationChannel{}, store.ErrNotFound
	}
	return channel, err
}

func (s *Store) ListNotificationChannels(ctx context.Context) ([]domain.NotificationChannel, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, type, enabled, send_resolved, repeat_interval_seconds, event_types,
		       project_ids, secret_ciphertext, created_at, updated_at, deleted_at
		FROM notification_channels WHERE deleted_at IS NULL ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.NotificationChannel
	for rows.Next() {
		channel, err := scanNotificationChannel(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, channel)
	}
	return result, rows.Err()
}

func (s *Store) UpdateNotificationChannel(ctx context.Context, channel domain.NotificationChannel) (domain.NotificationChannel, error) {
	eventTypes, _ := json.Marshal(channel.EventTypes)
	projectIDs, _ := json.Marshal(channel.ProjectIDs)
	updated, err := scanNotificationChannel(s.pool.QueryRow(ctx, `
		UPDATE notification_channels
		SET name=$2, type=$3, enabled=$4, send_resolved=$5, repeat_interval_seconds=$6,
		    event_types=$7, project_ids=$8, secret_ciphertext=$9, updated_at=$10
		WHERE id=$1 AND deleted_at IS NULL
		RETURNING id, name, type, enabled, send_resolved, repeat_interval_seconds, event_types,
		          project_ids, secret_ciphertext, created_at, updated_at, deleted_at`,
		channel.ID, channel.Name, channel.Type, channel.Enabled, channel.SendResolved,
		channel.RepeatIntervalSeconds, eventTypes, projectIDs, channel.SecretCiphertext, channel.UpdatedAt))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.NotificationChannel{}, store.ErrNotFound
	}
	return updated, mapError(err)
}

func (s *Store) ArchiveNotificationChannel(ctx context.Context, id string, at time.Time) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE notification_channels SET enabled=FALSE, deleted_at=$2, updated_at=$2
		WHERE id=$1 AND deleted_at IS NULL`, id, at)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) CreateAlertIncident(ctx context.Context, alert domain.AlertIncident) (domain.AlertIncident, error) {
	created, err := scanAlertIncident(s.pool.QueryRow(ctx, `
		INSERT INTO alert_incidents
			(id, fingerprint, kind, project_id, project_name, status, severity, summary, description,
			 source_event_id, occurrence_count, started_at, updated_at, resolved_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		RETURNING id, fingerprint, kind, project_id, project_name, status, severity, summary,
		          description, source_event_id, occurrence_count, started_at, updated_at, resolved_at`,
		alert.ID, alert.Fingerprint, alert.Kind, alert.ProjectID, alert.ProjectName, alert.Status,
		alert.Severity, alert.Summary, alert.Description, alert.SourceEventID, alert.OccurrenceCount,
		alert.StartedAt, alert.UpdatedAt, alert.ResolvedAt))
	return created, mapError(err)
}

func (s *Store) GetAlertIncident(ctx context.Context, id string) (domain.AlertIncident, error) {
	alert, err := scanAlertIncident(s.pool.QueryRow(ctx, `
		SELECT id, fingerprint, kind, project_id, project_name, status, severity, summary,
		       description, source_event_id, occurrence_count, started_at, updated_at, resolved_at
		FROM alert_incidents WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.AlertIncident{}, store.ErrNotFound
	}
	return alert, err
}

func (s *Store) GetFiringAlertIncident(ctx context.Context, fingerprint string) (domain.AlertIncident, error) {
	alert, err := scanAlertIncident(s.pool.QueryRow(ctx, `
		SELECT id, fingerprint, kind, project_id, project_name, status, severity, summary,
		       description, source_event_id, occurrence_count, started_at, updated_at, resolved_at
		FROM alert_incidents WHERE fingerprint=$1 AND status='firing'`, fingerprint))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.AlertIncident{}, store.ErrNotFound
	}
	return alert, err
}

func (s *Store) UpdateAlertIncident(ctx context.Context, alert domain.AlertIncident) (domain.AlertIncident, error) {
	updated, err := scanAlertIncident(s.pool.QueryRow(ctx, `
		UPDATE alert_incidents
		SET project_name=$2, status=$3, severity=$4, summary=$5, description=$6,
		    source_event_id=$7, occurrence_count=$8, updated_at=$9, resolved_at=$10
		WHERE id=$1
		RETURNING id, fingerprint, kind, project_id, project_name, status, severity, summary,
		          description, source_event_id, occurrence_count, started_at, updated_at, resolved_at`,
		alert.ID, alert.ProjectName, alert.Status, alert.Severity, alert.Summary, alert.Description,
		alert.SourceEventID, alert.OccurrenceCount, alert.UpdatedAt, alert.ResolvedAt))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.AlertIncident{}, store.ErrNotFound
	}
	return updated, err
}

func (s *Store) ListAlertIncidents(ctx context.Context, limit int) ([]domain.AlertIncident, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, fingerprint, kind, project_id, project_name, status, severity, summary,
		       description, source_event_id, occurrence_count, started_at, updated_at, resolved_at
		FROM alert_incidents ORDER BY updated_at DESC, id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.AlertIncident
	for rows.Next() {
		alert, err := scanAlertIncident(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, alert)
	}
	return result, rows.Err()
}

func (s *Store) CreateNotificationDelivery(ctx context.Context, delivery domain.NotificationDelivery) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO notification_deliveries
			(id, alert_id, channel_id, transition, dedupe_key, status, attempt_count,
			 next_attempt_at, lease_until, last_error, created_at, sent_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, delivery.ID, delivery.AlertID,
		delivery.ChannelID, delivery.Transition, delivery.DedupeKey, delivery.Status,
		delivery.AttemptCount, delivery.NextAttemptAt, delivery.LeaseUntil, delivery.LastError,
		delivery.CreatedAt, delivery.SentAt)
	return mapError(err)
}

func (s *Store) ClaimNotificationDeliveries(ctx context.Context, now, leaseUntil time.Time, limit int) ([]domain.NotificationDelivery, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx, `
		WITH candidates AS (
			SELECT id FROM notification_deliveries
			WHERE (status='pending' AND next_attempt_at <= $1)
			   OR (status='delivering' AND lease_until <= $1)
			ORDER BY created_at
			FOR UPDATE SKIP LOCKED LIMIT $2
		)
		UPDATE notification_deliveries AS delivery
		SET status='delivering', attempt_count=delivery.attempt_count+1, lease_until=$3
		FROM candidates WHERE delivery.id=candidates.id
		RETURNING delivery.id, delivery.alert_id, delivery.channel_id, delivery.transition,
		          delivery.dedupe_key, delivery.status, delivery.attempt_count, delivery.next_attempt_at,
		          delivery.lease_until, delivery.last_error, delivery.created_at, delivery.sent_at`, now, limit, leaseUntil)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.NotificationDelivery
	for rows.Next() {
		delivery, err := scanNotificationDelivery(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, delivery)
	}
	return result, rows.Err()
}

func (s *Store) CompleteNotificationDelivery(ctx context.Context, id string, sent bool, lastError string, completedAt, nextAttemptAt time.Time) error {
	status := "failed"
	var sentAt *time.Time
	if sent {
		status = "sent"
		sentAt = &completedAt
	} else if !nextAttemptAt.IsZero() {
		status = "pending"
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE notification_deliveries
		SET status=$2, lease_until=NULL, last_error=$3, next_attempt_at=CASE WHEN $4::timestamptz IS NULL THEN next_attempt_at ELSE $4 END, sent_at=$5
		WHERE id=$1`, id, status, lastError, nullableTime(nextAttemptAt), sentAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) ListNotificationDeliveries(ctx context.Context, limit int) ([]domain.NotificationDelivery, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT delivery.id, delivery.alert_id, delivery.channel_id, delivery.transition,
		       delivery.dedupe_key, delivery.status, delivery.attempt_count, delivery.next_attempt_at,
		       delivery.lease_until, delivery.last_error, delivery.created_at, delivery.sent_at,
		       channel.name
		FROM notification_deliveries AS delivery
		JOIN notification_channels AS channel ON channel.id=delivery.channel_id
		ORDER BY delivery.created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.NotificationDelivery
	for rows.Next() {
		var delivery domain.NotificationDelivery
		if err := rows.Scan(&delivery.ID, &delivery.AlertID, &delivery.ChannelID, &delivery.Transition,
			&delivery.DedupeKey, &delivery.Status, &delivery.AttemptCount, &delivery.NextAttemptAt,
			&delivery.LeaseUntil, &delivery.LastError, &delivery.CreatedAt, &delivery.SentAt,
			&delivery.ChannelName); err != nil {
			return nil, err
		}
		result = append(result, delivery)
	}
	return result, rows.Err()
}

func (s *Store) AppendAuditEvent(ctx context.Context, event domain.AuditEvent) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO audit_events
			(id, actor, action, resource_type, resource_id, outcome, client_ip, status_code, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		event.ID, event.Actor, event.Action, event.ResourceType, event.ResourceID,
		event.Outcome, event.ClientIP, event.StatusCode, event.CreatedAt)
	return mapError(err)
}

func (s *Store) ListAuditEvents(ctx context.Context, limit int) ([]domain.AuditEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, actor, action, resource_type, resource_id, outcome, client_ip, status_code, created_at
		FROM audit_events ORDER BY created_at DESC, id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.AuditEvent
	for rows.Next() {
		var event domain.AuditEvent
		if err := rows.Scan(&event.ID, &event.Actor, &event.Action, &event.ResourceType,
			&event.ResourceID, &event.Outcome, &event.ClientIP, &event.StatusCode,
			&event.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, event)
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
		  (SELECT COUNT(*) FROM runs WHERE started_at >= $1 AND COALESCE(stats->>'operation', 'backup') = 'backup' AND status = $2),
		  (SELECT COUNT(*) FROM runs WHERE started_at >= $1 AND COALESCE(stats->>'operation', 'backup') = 'backup' AND status IN ($3, $4, $5)),
		  (SELECT COUNT(*) FROM runs WHERE started_at >= $1 AND COALESCE(stats->>'operation', 'backup') = 'backup' AND status = $6)`,
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
	var sources, schedule, policy []byte
	err := row.Scan(&project.ID, &project.ServerID, &project.RepositoryID, &project.Name,
		&project.Enabled, &sources, &schedule, &policy, &project.Revision, &project.CreatedAt, &project.UpdatedAt)
	if err != nil {
		return domain.Project{}, err
	}
	if err := json.Unmarshal(sources, &project.Sources); err != nil {
		return domain.Project{}, err
	}
	if err := json.Unmarshal(schedule, &project.Schedule); err != nil {
		return domain.Project{}, err
	}
	if err := json.Unmarshal(policy, &project.Policy); err != nil {
		return domain.Project{}, err
	}
	return project, nil
}

func scanSnapshot(row scanner) (domain.Snapshot, error) {
	var snapshot domain.Snapshot
	var paths, tags []byte
	if err := row.Scan(&snapshot.ID, &snapshot.ProjectID, &snapshot.ServerID, &snapshot.Time,
		&snapshot.Hostname, &snapshot.Username, &paths, &tags, &snapshot.TotalFiles,
		&snapshot.TotalBytes, &snapshot.Protected, &snapshot.LastSyncedAt); err != nil {
		return domain.Snapshot{}, err
	}
	if err := json.Unmarshal(paths, &snapshot.Paths); err != nil {
		return domain.Snapshot{}, err
	}
	if err := json.Unmarshal(tags, &snapshot.Tags); err != nil {
		return domain.Snapshot{}, err
	}
	return snapshot, nil
}

func scanNotificationChannel(row scanner) (domain.NotificationChannel, error) {
	var channel domain.NotificationChannel
	var eventTypes, projectIDs []byte
	err := row.Scan(&channel.ID, &channel.Name, &channel.Type, &channel.Enabled,
		&channel.SendResolved, &channel.RepeatIntervalSeconds, &eventTypes, &projectIDs,
		&channel.SecretCiphertext, &channel.CreatedAt, &channel.UpdatedAt, &channel.DeletedAt)
	if err != nil {
		return domain.NotificationChannel{}, err
	}
	if err := json.Unmarshal(eventTypes, &channel.EventTypes); err != nil {
		return domain.NotificationChannel{}, err
	}
	if err := json.Unmarshal(projectIDs, &channel.ProjectIDs); err != nil {
		return domain.NotificationChannel{}, err
	}
	return channel, nil
}

func scanAlertIncident(row scanner) (domain.AlertIncident, error) {
	var alert domain.AlertIncident
	err := row.Scan(&alert.ID, &alert.Fingerprint, &alert.Kind, &alert.ProjectID,
		&alert.ProjectName, &alert.Status, &alert.Severity, &alert.Summary,
		&alert.Description, &alert.SourceEventID, &alert.OccurrenceCount,
		&alert.StartedAt, &alert.UpdatedAt, &alert.ResolvedAt)
	return alert, err
}

func scanNotificationDelivery(row scanner) (domain.NotificationDelivery, error) {
	var delivery domain.NotificationDelivery
	err := row.Scan(&delivery.ID, &delivery.AlertID, &delivery.ChannelID, &delivery.Transition,
		&delivery.DedupeKey, &delivery.Status, &delivery.AttemptCount, &delivery.NextAttemptAt,
		&delivery.LeaseUntil, &delivery.LastError, &delivery.CreatedAt, &delivery.SentAt)
	return delivery, err
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

func publicRepository(repository domain.Repository) domain.Repository {
	repository.SecretCiphertext = nil
	repository.Password = ""
	repository.Environment = nil
	repository.Options = nil
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
