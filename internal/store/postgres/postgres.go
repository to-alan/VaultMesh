package postgres

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
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

func (s *Store) Dashboard(ctx context.Context, since time.Time) (domain.Dashboard, error) {
	var dashboard domain.Dashboard
	err := s.pool.QueryRow(ctx, `
		SELECT
		  (SELECT COUNT(*) FROM servers),
		  (SELECT COUNT(*) FROM servers WHERE last_seen_at >= NOW() - $2::interval),
		  (SELECT COUNT(*) FROM projects),
		  (SELECT COUNT(*) FROM runs WHERE started_at >= $1 AND COALESCE(stats->>'operation', 'backup') = 'backup' AND status = $3),
		  (SELECT COUNT(*) FROM runs WHERE started_at >= $1 AND COALESCE(stats->>'operation', 'backup') = 'backup' AND status IN ($4, $5, $6)),
		  (SELECT COUNT(*) FROM runs WHERE started_at >= $1 AND COALESCE(stats->>'operation', 'backup') = 'backup' AND status = $7)`,
		since, fmt.Sprintf("%d seconds", int64(domain.AgentOfflineAfter/time.Second)), domain.RunSucceeded, domain.RunFailed, domain.RunTimedOut,
		domain.RunUnknown, domain.RunPartial).Scan(&dashboard.ServersTotal,
		&dashboard.ServersOnline, &dashboard.ProjectsTotal, &dashboard.RunsSucceeded,
		&dashboard.RunsFailed, &dashboard.RunsPartial)
	return dashboard, err
}
