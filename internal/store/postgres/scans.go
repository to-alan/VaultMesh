package postgres

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/to-alan/vaultmesh/internal/domain"
	"github.com/to-alan/vaultmesh/internal/store"
)

// Shared row scanners and error mapping helpers.

type scanner interface {
	Scan(...any) error
}

func scanServer(row scanner) (domain.Server, error) {
	var server domain.Server
	err := row.Scan(&server.ID, &server.Name, &server.Hostname, &server.OS, &server.Arch,
		&server.AgentVersion, &server.Status, &server.LastSeenAt, &server.DesiredRevision,
		&server.AppliedRevision, &server.CreatedAt, &server.ArchivedAt)
	return server, err
}

func getServer(ctx context.Context, tx pgx.Tx, id string) (domain.Server, error) {
	return scanServer(tx.QueryRow(ctx, `
		SELECT id, name, hostname, os, arch, agent_version, status, last_seen_at,
		       desired_revision, applied_revision, created_at, archived_at
		FROM servers WHERE id = $1`, id))
}

func scanProject(row scanner) (domain.Project, error) {
	var project domain.Project
	var sources, schedule, policy []byte
	err := row.Scan(&project.ID, &project.ServerID, &project.RepositoryID, &project.Name,
		&project.Enabled, &sources, &schedule, &policy, &project.Revision, &project.CreatedAt, &project.UpdatedAt,
		&project.ArchivedAt)
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
	var eventTypes, projectIDs, serverIDs []byte
	err := row.Scan(&channel.ID, &channel.Name, &channel.Type, &channel.Enabled,
		&channel.SendResolved, &channel.RepeatIntervalSeconds, &eventTypes, &projectIDs, &serverIDs,
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
	if err := json.Unmarshal(serverIDs, &channel.ServerIDs); err != nil {
		return domain.NotificationChannel{}, err
	}
	return channel, nil
}

func scanAlertIncident(row scanner) (domain.AlertIncident, error) {
	var alert domain.AlertIncident
	err := row.Scan(&alert.ID, &alert.Fingerprint, &alert.Kind, &alert.ResourceType,
		&alert.ResourceID, &alert.ResourceName, &alert.ProjectID, &alert.ProjectName,
		&alert.Status, &alert.Severity, &alert.Summary,
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

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
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
