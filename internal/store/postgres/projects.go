package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/to-alan/vaultmesh/internal/domain"
	"github.com/to-alan/vaultmesh/internal/store"
)

// Backup projects, the agent desired-config projection, commands, and snapshot indexes.

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
		       revision, created_at, updated_at, archived_at
		FROM projects WHERE id = $1 AND archived_at IS NULL`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Project{}, store.ErrNotFound
	}
	return project, err
}

func (s *Store) ListProjects(ctx context.Context) ([]domain.Project, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, server_id, repository_id, name, enabled, sources, schedule, policy,
		       revision, created_at, updated_at, archived_at
		FROM projects WHERE archived_at IS NULL ORDER BY created_at`)
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
		       revision, created_at, updated_at, archived_at
		FROM projects WHERE id = $1 AND archived_at IS NULL FOR UPDATE`, project.ID))
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
		          revision, created_at, updated_at, archived_at`, project.ID, project.Name, sources,
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
			       revision, created_at, updated_at, archived_at FROM projects WHERE id = $1`, id))
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
		          revision, created_at, updated_at, archived_at`, id, enabled, revision, updatedAt))
	if err != nil {
		return domain.Project{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Project{}, err
	}
	return project, nil
}

func (s *Store) ArchiveProject(ctx context.Context, id string, archivedAt time.Time) (domain.Project, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Project{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var serverID string
	if err := tx.QueryRow(ctx, `
		SELECT server_id FROM projects WHERE id = $1 AND archived_at IS NULL FOR UPDATE`, id).Scan(&serverID); errors.Is(err, pgx.ErrNoRows) {
		return domain.Project{}, store.ErrNotFound
	} else if err != nil {
		return domain.Project{}, err
	}
	// The revision bump removes the project from the Agent's DesiredConfig on
	// its next successful sync; run history and snapshot indexes keep their
	// references for auditing and recovery evidence.
	var revision int64
	if err := tx.QueryRow(ctx, `
		UPDATE servers SET desired_revision = desired_revision + 1
		WHERE id = $1 RETURNING desired_revision`, serverID).Scan(&revision); err != nil {
		return domain.Project{}, err
	}
	project, err := scanProject(tx.QueryRow(ctx, `
		UPDATE projects SET archived_at = $2, enabled = FALSE, revision = $3, updated_at = $4
		WHERE id = $1
		RETURNING id, server_id, repository_id, name, enabled, sources, schedule, policy,
		          revision, created_at, updated_at, archived_at`, id, archivedAt, revision, archivedAt))
	if err != nil {
		return domain.Project{}, mapError(err)
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
		JOIN repositories r ON r.id = p.repository_id AND r.archived_at IS NULL
		WHERE p.server_id = $1 AND p.enabled = TRUE AND p.archived_at IS NULL
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
	var created domain.Command
	if command.ProjectID == "" {
		// Server-scoped commands (detection) are not bound to a project.
		err = s.pool.QueryRow(ctx, `
			INSERT INTO commands (id, server_id, type, payload, created_at)
			SELECT $1, id, $3, $4, $5
			FROM servers
			WHERE id = $2 AND archived_at IS NULL
			RETURNING server_id`, command.ID, command.ServerID, command.Type, payload, command.CreatedAt).Scan(&created.ServerID)
	} else {
		err = s.pool.QueryRow(ctx, `
			INSERT INTO commands (id, server_id, project_id, type, payload, created_at)
			SELECT $1, server_id, id, $3, $4, $5
			FROM projects
			WHERE id = $2 AND enabled = TRUE
			RETURNING server_id`, command.ID, command.ProjectID, command.Type, payload, command.CreatedAt).Scan(&created.ServerID)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Command{}, store.ErrNotFound
	}
	if err != nil {
		return domain.Command{}, mapError(err)
	}
	created.ID = command.ID
	created.ProjectID = command.ProjectID
	created.Type = command.Type
	created.Payload = command.Payload
	created.CreatedAt = command.CreatedAt
	return created, nil
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
		RETURNING c.id, c.server_id, COALESCE(c.project_id, ''), c.type, c.payload, c.leased_until, c.attempts, c.created_at`,
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

// SaveDetectionReport stores the latest detection inventory for a server and
// closes the originating server-scoped command in the same transaction.
func (s *Store) SaveDetectionReport(ctx context.Context, serverID, commandID string, report domain.DetectionReport, at time.Time) error {
	encoded, err := json.Marshal(report)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `
		INSERT INTO detection_reports (server_id, command_id, report, detected_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (server_id) DO UPDATE SET
		    command_id = EXCLUDED.command_id,
		    report = EXCLUDED.report,
		    detected_at = EXCLUDED.detected_at`, serverID, commandID, encoded, at); err != nil {
		return mapError(err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE commands
		SET accepted_at = COALESCE(accepted_at, $3), completed_at = $3
		WHERE id = $1 AND server_id = $2`, commandID, serverID, at); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) GetDetectionReport(ctx context.Context, serverID string) (domain.DetectionReport, bool, error) {
	var encoded []byte
	var detectedAt time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT report, detected_at FROM detection_reports WHERE server_id = $1`, serverID).Scan(&encoded, &detectedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.DetectionReport{}, false, nil
	}
	if err != nil {
		return domain.DetectionReport{}, false, mapError(err)
	}
	var report domain.DetectionReport
	if err := json.Unmarshal(encoded, &report); err != nil {
		return domain.DetectionReport{}, false, err
	}
	return report, true, nil
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
