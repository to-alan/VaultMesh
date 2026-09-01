package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/to-alan/vaultmesh/internal/domain"
	"github.com/to-alan/vaultmesh/internal/store"
)

// Server enrollment, credentials, and global repository channels.

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
		SELECT e.server_id, e.expires_at, e.used_at
		FROM enrollments e
		JOIN servers s ON s.id = e.server_id
		WHERE e.token_hash = $1 AND s.archived_at IS NULL
		FOR UPDATE OF e`, enrollmentHash).Scan(&serverID, &expiresAt, &usedAt)
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
		       s.status, s.last_seen_at, s.desired_revision, s.applied_revision, s.created_at, s.archived_at
		FROM agent_credentials a
		JOIN servers s ON s.id = a.server_id
		WHERE a.token_hash = $1 AND a.revoked_at IS NULL AND s.archived_at IS NULL`, credentialHash)
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
		       CASE WHEN last_seen_at IS NOT NULL AND last_seen_at < NOW() - $2::interval
		            THEN $1 ELSE status END,
		       last_seen_at, desired_revision, applied_revision, created_at, archived_at
		FROM servers
		WHERE archived_at IS NULL
		ORDER BY created_at`, domain.ServerOffline, fmt.Sprintf("%d seconds", int64(domain.AgentOfflineAfter/time.Second)))
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

func (s *Store) ArchiveServer(ctx context.Context, id string, archivedAt time.Time) (domain.Server, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Server{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var existingArchived *time.Time
	if err := tx.QueryRow(ctx, `SELECT archived_at FROM servers WHERE id = $1 FOR UPDATE`, id).Scan(&existingArchived); errors.Is(err, pgx.ErrNoRows) {
		return domain.Server{}, store.ErrNotFound
	} else if err != nil {
		return domain.Server{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE agent_credentials SET revoked_at = $2
		WHERE server_id = $1 AND revoked_at IS NULL`, id, archivedAt); err != nil {
		return domain.Server{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE servers SET status = $2, archived_at = $3 WHERE id = $1`, id, domain.ServerOffline, archivedAt); err != nil {
		return domain.Server{}, err
	}
	server, err := getServer(ctx, tx, id)
	if err != nil {
		return domain.Server{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Server{}, err
	}
	return server, nil
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
		SELECT id, provider, name, url, created_at, archived_at
		FROM repositories WHERE archived_at IS NULL ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.Repository
	for rows.Next() {
		var repository domain.Repository
		if err := rows.Scan(&repository.ID, &repository.Provider, &repository.Name, &repository.URL, &repository.CreatedAt, &repository.ArchivedAt); err != nil {
			return nil, err
		}
		result = append(result, repository)
	}
	return result, rows.Err()
}

func (s *Store) GetRepository(ctx context.Context, id string) (domain.Repository, error) {
	var repository domain.Repository
	err := s.pool.QueryRow(ctx, `
		SELECT id, provider, name, url, secret_ciphertext, created_at, archived_at
		FROM repositories WHERE id = $1 AND archived_at IS NULL`, id).Scan(&repository.ID, &repository.Provider,
		&repository.Name, &repository.URL, &repository.SecretCiphertext, &repository.CreatedAt, &repository.ArchivedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Repository{}, store.ErrNotFound
	}
	return repository, err
}

func (s *Store) ArchiveRepository(ctx context.Context, id string, archivedAt time.Time) (domain.Repository, error) {
	var repository domain.Repository
	err := s.pool.QueryRow(ctx, `
		UPDATE repositories SET archived_at = $2
		WHERE id = $1 AND archived_at IS NULL
		RETURNING id, provider, name, url, created_at, archived_at`,
		id, archivedAt).Scan(&repository.ID, &repository.Provider,
		&repository.Name, &repository.URL, &repository.CreatedAt, &repository.ArchivedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Repository{}, store.ErrNotFound
	}
	return repository, mapError(err)
}
