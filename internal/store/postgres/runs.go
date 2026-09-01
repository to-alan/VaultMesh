package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/to-alan/vaultmesh/internal/domain"
	"github.com/to-alan/vaultmesh/internal/store"
)

// Run reports and control-plane retention pruning.

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

// PruneBefore deletes expired operational facts from one scope. Runs and audit
// events are plain deletes; deliveries cascade from incidents so a single
// incident delete also removes its delivery history. Pending notification
// work and firing incidents are never removed, regardless of age.

func (s *Store) PruneBefore(ctx context.Context, scope store.RetentionScope, before time.Time) (int64, error) {
	var query string
	switch scope {
	case store.RetentionRuns:
		query = `WITH removed AS (DELETE FROM runs WHERE started_at < $1 AND status <> 'running' RETURNING 1) SELECT COUNT(*) FROM removed`
	case store.RetentionCommands:
		query = `WITH removed AS (DELETE FROM commands WHERE created_at < $1 AND completed_at IS NOT NULL RETURNING 1) SELECT COUNT(*) FROM removed`
	case store.RetentionDeliveries:
		query = `WITH removed AS (DELETE FROM notification_deliveries WHERE created_at < $1 AND status IN ('sent', 'failed') RETURNING 1) SELECT COUNT(*) FROM removed`
	case store.RetentionIncidents:
		query = `WITH removed AS (DELETE FROM alert_incidents WHERE updated_at < $1 AND status = 'resolved' RETURNING 1) SELECT COUNT(*) FROM removed`
	case store.RetentionAudit:
		query = `WITH removed AS (DELETE FROM audit_events WHERE created_at < $1 RETURNING 1) SELECT COUNT(*) FROM removed`
	default:
		return 0, fmt.Errorf("unknown retention scope %q", scope)
	}
	tag, err := s.pool.Exec(ctx, query, before)
	if err != nil {
		return 0, mapError(err)
	}
	return tag.RowsAffected(), nil
}
