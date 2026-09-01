// Package postgres: notification channels, alert incidents, delivery
// queue, and audit event persistence.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/to-alan/vaultmesh/internal/domain"
	"github.com/to-alan/vaultmesh/internal/store"
)

// Notification channels, alert incidents, delivery queue, and audit events.

func (s *Store) CreateNotificationChannel(ctx context.Context, channel domain.NotificationChannel) (domain.NotificationChannel, error) {
	eventTypes, _ := json.Marshal(channel.EventTypes)
	projectIDs, _ := json.Marshal(channel.ProjectIDs)
	serverIDs, _ := json.Marshal(channel.ServerIDs)
	created, err := scanNotificationChannel(s.pool.QueryRow(ctx, `
		INSERT INTO notification_channels
			(id, name, type, enabled, send_resolved, repeat_interval_seconds, event_types, project_ids,
			 server_ids, secret_ciphertext, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		RETURNING id, name, type, enabled, send_resolved, repeat_interval_seconds, event_types,
		          project_ids, server_ids, secret_ciphertext, created_at, updated_at, deleted_at`,
		channel.ID, channel.Name, channel.Type, channel.Enabled, channel.SendResolved,
		channel.RepeatIntervalSeconds, eventTypes, projectIDs, serverIDs, channel.SecretCiphertext,
		channel.CreatedAt, channel.UpdatedAt))
	return created, mapError(err)
}

func (s *Store) GetNotificationChannel(ctx context.Context, id string) (domain.NotificationChannel, error) {
	channel, err := scanNotificationChannel(s.pool.QueryRow(ctx, `
		SELECT id, name, type, enabled, send_resolved, repeat_interval_seconds, event_types,
		       project_ids, server_ids, secret_ciphertext, created_at, updated_at, deleted_at
		FROM notification_channels WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.NotificationChannel{}, store.ErrNotFound
	}
	return channel, err
}

func (s *Store) ListNotificationChannels(ctx context.Context) ([]domain.NotificationChannel, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, type, enabled, send_resolved, repeat_interval_seconds, event_types,
		       project_ids, server_ids, secret_ciphertext, created_at, updated_at, deleted_at
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
	serverIDs, _ := json.Marshal(channel.ServerIDs)
	updated, err := scanNotificationChannel(s.pool.QueryRow(ctx, `
		UPDATE notification_channels
		SET name=$2, type=$3, enabled=$4, send_resolved=$5, repeat_interval_seconds=$6,
		    event_types=$7, project_ids=$8, server_ids=$9, secret_ciphertext=$10, updated_at=$11
		WHERE id=$1 AND deleted_at IS NULL
		RETURNING id, name, type, enabled, send_resolved, repeat_interval_seconds, event_types,
		          project_ids, server_ids, secret_ciphertext, created_at, updated_at, deleted_at`,
		channel.ID, channel.Name, channel.Type, channel.Enabled, channel.SendResolved,
		channel.RepeatIntervalSeconds, eventTypes, projectIDs, serverIDs, channel.SecretCiphertext, channel.UpdatedAt))
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
	if alert.ResourceType == "" && alert.ProjectID != "" {
		alert.ResourceType, alert.ResourceID, alert.ResourceName = "project", alert.ProjectID, alert.ProjectName
	}
	created, err := scanAlertIncident(s.pool.QueryRow(ctx, `
		INSERT INTO alert_incidents
			(id, fingerprint, kind, resource_type, resource_id, resource_name, project_id, project_name,
			 status, severity, summary, description, source_event_id, occurrence_count, started_at, updated_at, resolved_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		RETURNING id, fingerprint, kind, resource_type, resource_id, resource_name,
		          COALESCE(project_id, ''), COALESCE(project_name, ''), status, severity, summary,
		          description, source_event_id, occurrence_count, started_at, updated_at, resolved_at`,
		alert.ID, alert.Fingerprint, alert.Kind, alert.ResourceType, alert.ResourceID, alert.ResourceName,
		nullableString(alert.ProjectID), nullableString(alert.ProjectName), alert.Status,
		alert.Severity, alert.Summary, alert.Description, alert.SourceEventID, alert.OccurrenceCount,
		alert.StartedAt, alert.UpdatedAt, alert.ResolvedAt))
	return created, mapError(err)
}

func (s *Store) GetAlertIncident(ctx context.Context, id string) (domain.AlertIncident, error) {
	alert, err := scanAlertIncident(s.pool.QueryRow(ctx, `
		SELECT id, fingerprint, kind, resource_type, resource_id, resource_name,
		       COALESCE(project_id, ''), COALESCE(project_name, ''), status, severity, summary,
		       description, source_event_id, occurrence_count, started_at, updated_at, resolved_at
		FROM alert_incidents WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.AlertIncident{}, store.ErrNotFound
	}
	return alert, err
}

func (s *Store) GetFiringAlertIncident(ctx context.Context, fingerprint string) (domain.AlertIncident, error) {
	alert, err := scanAlertIncident(s.pool.QueryRow(ctx, `
		SELECT id, fingerprint, kind, resource_type, resource_id, resource_name,
		       COALESCE(project_id, ''), COALESCE(project_name, ''), status, severity, summary,
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
		SET resource_name=$2, project_name=$3, status=$4, severity=$5, summary=$6, description=$7,
		    source_event_id=$8, occurrence_count=$9, updated_at=$10, resolved_at=$11
		WHERE id=$1
		RETURNING id, fingerprint, kind, resource_type, resource_id, resource_name,
		          COALESCE(project_id, ''), COALESCE(project_name, ''), status, severity, summary,
		          description, source_event_id, occurrence_count, started_at, updated_at, resolved_at`,
		alert.ID, alert.ResourceName, nullableString(alert.ProjectName), alert.Status, alert.Severity, alert.Summary, alert.Description,
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
		SELECT id, fingerprint, kind, resource_type, resource_id, resource_name,
		       COALESCE(project_id, ''), COALESCE(project_name, ''), status, severity, summary,
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
