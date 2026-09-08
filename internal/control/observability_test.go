package control

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/to-alan/vaultmesh/internal/domain"
	"github.com/to-alan/vaultmesh/internal/secret"
	"github.com/to-alan/vaultmesh/internal/store"
	"github.com/to-alan/vaultmesh/internal/store/memory"
	"github.com/to-alan/vaultmesh/internal/store/postgres"
)

// Run identical behavior checks against both storage implementations. A local
// unit run uses memory; CI also provides a disposable PostgreSQL database.
func TestBackupObservability(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			ctx := context.Background()
			var data store.Store = memory.New()
			if backend == "postgres" {
				databaseURL := os.Getenv("VAULTMESH_TEST_DATABASE_URL")
				if databaseURL == "" {
					t.Skip("VAULTMESH_TEST_DATABASE_URL is not configured")
				}
				// Keep this suite separate from other packages that migrate and
				// seed the same CI database concurrently.
				connection, err := pgx.Connect(ctx, databaseURL)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = connection.Close(context.Background()) })
				schema := fmt.Sprintf("observability_%d", time.Now().UnixNano())
				quoted := pgx.Identifier{schema}.Sanitize()
				if _, err := connection.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if _, err := connection.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
						t.Error(err)
					}
				})
				parsed, err := url.Parse(databaseURL)
				if err != nil {
					t.Fatal(err)
				}
				query := parsed.Query()
				query.Set("search_path", schema)
				parsed.RawQuery = query.Encode()
				pg, err := postgres.Open(ctx, parsed.String())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(pg.Close)
				if err := pg.Migrate(ctx); err != nil {
					t.Fatal(err)
				}
				data = pg
			}
			sealer, err := secret.New(bytes.Repeat([]byte{23}, 32))
			if err != nil {
				t.Fatal(err)
			}
			service := NewService(data, sealer)
			suffix := fmt.Sprint(time.Now().UnixNano())
			enrollment, err := service.CreateServer(ctx, "Observability "+suffix)
			if err != nil {
				t.Fatal(err)
			}
			repository, err := service.CreateRepository(ctx, domain.Repository{Provider: "local", Name: "Observability " + suffix, URL: "/tmp/observability", Password: "test-only"})
			if err != nil {
				t.Fatal(err)
			}
			base := time.Now().UTC().Truncate(24 * time.Hour)
			current := base
			service.now = func() time.Time { return current }
			project, err := service.CreateProject(ctx, domain.Project{
				ServerID: enrollment.Server.ID, RepositoryID: repository.ID, Name: "Observability project",
				Sources:  []domain.Source{{Type: "files", Paths: []string{"/srv/app"}, Required: true}},
				Schedule: domain.Schedule{Cron: "0 1 * * *", Timezone: "UTC", MaxRuntimeSeconds: 3600, GraceSeconds: 1800},
			})
			if err != nil {
				t.Fatal(err)
			}
			save := func(id, status string, at time.Time, stats map[string]any) domain.RunReport {
				t.Helper()
				report := domain.RunReport{ID: id + suffix, IdempotencyKey: id + suffix, ProjectID: project.ID, ServerID: project.ServerID, ScheduledAt: at, StartedAt: at, Status: status, Stats: stats}
				if status != domain.RunRunning {
					finished := at.Add(time.Second)
					report.FinishedAt = &finished
				}
				if err := data.UpsertRun(ctx, report); err != nil {
					t.Fatal(err)
				}
				return report
			}
			assertHealth := func(status string) {
				t.Helper()
				items, err := service.ProjectHealth(ctx)
				if err != nil {
					t.Fatal(err)
				}
				for _, item := range items {
					if item.ProjectID == project.ID {
						if item.Status != status {
							t.Fatalf("health = %#v, want %s", item, status)
						}
						return
					}
				}
				t.Fatal("project health is missing")
			}
			current = base.Add(90 * time.Minute)
			active := save("run_active", domain.RunRunning, base.Add(65*time.Minute), map[string]any{"operation": "backup"})
			save("run_skipped", domain.RunSkipped, base.Add(70*time.Minute), map[string]any{"operation": "backup"})
			assertHealth("running")
			current = base.Add(151 * time.Minute)
			assertHealth("overdue")
			current = base.Add(90 * time.Minute)
			active.Status, active.FinishedAt = domain.RunFailed, &current
			if err := data.UpsertRun(ctx, active); err != nil {
				t.Fatal(err)
			}
			assertHealth("late")
			assertFailureAlert := func(wantFiring bool) {
				t.Helper()
				if err := service.EvaluateAlerts(ctx); err != nil {
					t.Fatal(err)
				}
				_, err := data.GetFiringAlertIncident(ctx, "backup:"+project.ID)
				if (wantFiring && err != nil) || (!wantFiring && !errors.Is(err, store.ErrNotFound)) {
					t.Fatalf("firing failure alert = %v, want %v", err, wantFiring)
				}
			}
			assertFailureAlert(true)
			save("run_retry", domain.RunRunning, current.Add(time.Minute), nil)
			assertFailureAlert(true)
			save("run_retry_skipped", domain.RunSkipped, current.Add(2*time.Minute), nil)
			assertFailureAlert(true)
			save("run_recovered", domain.RunSucceeded, current.Add(3*time.Minute), nil)
			assertFailureAlert(false)

			before, err := data.Dashboard(ctx, base)
			if err != nil {
				t.Fatal(err)
			}
			for i, item := range []struct{ status, operation string }{
				{domain.RunSucceeded, ""}, {domain.RunSucceeded, "backup"},
				{domain.RunPartial, "backup"}, {domain.RunCanceled, "backup"},
				{domain.RunTimedOut, "backup"}, {domain.RunUnknown, "backup"},
				{domain.RunSkipped, "backup"}, {domain.RunRunning, "backup"},
				{domain.RunSucceeded, "verification"}, {domain.RunFailed, "snapshot_restore"},
			} {
				save(fmt.Sprintf("run_metric_%d", i), item.status, current, map[string]any{"operation": item.operation})
			}
			save("run_legacy", domain.RunSucceeded, current, nil)
			save("run_old", domain.RunFailed, base.Add(-time.Hour), nil)
			after, err := data.Dashboard(ctx, base)
			if err != nil {
				t.Fatal(err)
			}
			if after.RunsSucceeded-before.RunsSucceeded != 3 || after.RunsPartial-before.RunsPartial != 1 || after.RunsFailed-before.RunsFailed != 3 {
				t.Fatalf("incorrect backup-only counts: before=%#v after=%#v", before, after)
			}
		})
	}
}
