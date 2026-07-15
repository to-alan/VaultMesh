package control

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/to-alan/vaultmesh/internal/domain"
	"github.com/to-alan/vaultmesh/internal/secret"
	"github.com/to-alan/vaultmesh/internal/store/memory"
)

func TestNotificationLifecycleDeduplicatesRepeatsAndSendsRecovery(t *testing.T) {
	ctx := context.Background()
	dataStore := memory.New()
	sealer, err := secret.New(bytes.Repeat([]byte{12}, 32))
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(dataStore, sealer)
	enrollment, err := service.CreateServer(ctx, "Notification host")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := service.EnrollAgent(ctx, enrollment.EnrollmentToken, domain.AgentInfo{Hostname: "notification-host"})
	if err != nil {
		t.Fatal(err)
	}
	repository, err := service.CreateRepository(ctx, domain.Repository{Provider: "local", Name: "Notification repository", URL: "/tmp/notification-repository", Password: "password"})
	if err != nil {
		t.Fatal(err)
	}
	current := time.Date(2026, time.July, 15, 0, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return current }
	project, err := service.CreateProject(ctx, domain.Project{
		ServerID: identity.AgentID, RepositoryID: repository.ID, Name: "Critical database",
		Sources:  []domain.Source{{Type: "files", Paths: []string{"/tmp"}, Required: true}},
		Schedule: domain.Schedule{Cron: "0 1 * * *", Timezone: "UTC", MaxRuntimeSeconds: 3600, GraceSeconds: 1800},
	})
	if err != nil {
		t.Fatal(err)
	}
	channel, err := service.CreateNotificationChannel(ctx, domain.NotificationChannel{
		Name: "Operations webhook", Type: "webhook", Enabled: true, SendResolved: true,
		RepeatIntervalSeconds: 4 * 60 * 60, EventTypes: []string{"backup_failure", "rpo_overdue"},
		Config: map[string]string{"url": "https://alerts.example.com/private-token", "method": "POST"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !channel.Configured || channel.Destination != "alerts.example.com" || channel.Config["url"] != "" {
		t.Fatalf("public channel leaked or omitted configuration state: %#v", channel)
	}
	var transitions []string
	var deliveredURL string
	service.notificationSender = func(_ context.Context, _ domain.NotificationChannel, config map[string]string, _ domain.AlertIncident, transition string) error {
		transitions = append(transitions, transition)
		deliveredURL = config["url"]
		return nil
	}
	updated, err := service.UpdateNotificationChannel(ctx, channel.ID, domain.NotificationChannel{
		Name: channel.Name, Type: channel.Type, Enabled: true, SendResolved: true,
		RepeatIntervalSeconds: channel.RepeatIntervalSeconds, EventTypes: channel.EventTypes,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.TestNotificationChannel(ctx, updated.ID); err != nil {
		t.Fatal(err)
	}
	if deliveredURL != "https://alerts.example.com/private-token" {
		t.Fatalf("blank secret fields did not preserve encrypted configuration: %q", deliveredURL)
	}
	transitions = nil

	current = time.Date(2026, time.July, 15, 2, 31, 0, 0, time.UTC)
	if err := service.EvaluateAlerts(ctx); err != nil {
		t.Fatal(err)
	}
	if err := service.DeliverNotifications(ctx); err != nil {
		t.Fatal(err)
	}
	if len(transitions) != 1 || transitions[0] != "firing" {
		t.Fatalf("unexpected initial notifications: %#v", transitions)
	}
	if err := service.EvaluateAlerts(ctx); err != nil {
		t.Fatal(err)
	}
	if err := service.DeliverNotifications(ctx); err != nil {
		t.Fatal(err)
	}
	if len(transitions) != 1 {
		t.Fatalf("unchanged incident was not deduplicated: %#v", transitions)
	}

	current = current.Add(4 * time.Hour)
	if err := service.EvaluateAlerts(ctx); err != nil {
		t.Fatal(err)
	}
	if err := service.DeliverNotifications(ctx); err != nil {
		t.Fatal(err)
	}
	if len(transitions) != 2 || transitions[1] != "repeat" {
		t.Fatalf("repeat interval did not produce one reminder: %#v", transitions)
	}

	started := current.Add(time.Minute)
	finished := started.Add(time.Minute)
	if err := dataStore.UpsertRun(ctx, domain.RunReport{
		ID: "run_notification_success", IdempotencyKey: "notification-success", ProjectID: project.ID, ServerID: identity.AgentID,
		ScheduledAt: started, StartedAt: started, FinishedAt: &finished, Status: domain.RunSucceeded,
		Stats: map[string]any{"operation": "backup"},
	}); err != nil {
		t.Fatal(err)
	}
	current = finished.Add(time.Minute)
	if err := service.EvaluateAlerts(ctx); err != nil {
		t.Fatal(err)
	}
	if err := service.DeliverNotifications(ctx); err != nil {
		t.Fatal(err)
	}
	if len(transitions) != 3 || transitions[2] != "resolved" {
		t.Fatalf("RPO recovery notification missing: %#v", transitions)
	}

	failedStarted := current.Add(time.Minute)
	failedFinished := failedStarted.Add(time.Minute)
	if err := dataStore.UpsertRun(ctx, domain.RunReport{
		ID: "run_notification_failed", IdempotencyKey: "notification-failed", ProjectID: project.ID, ServerID: identity.AgentID,
		ScheduledAt: failedStarted, StartedAt: failedStarted, FinishedAt: &failedFinished, Status: domain.RunFailed,
		Stats: map[string]any{"operation": "backup"},
	}); err != nil {
		t.Fatal(err)
	}
	current = failedFinished.Add(time.Minute)
	if err := service.EvaluateAlerts(ctx); err != nil {
		t.Fatal(err)
	}
	if err := service.DeliverNotifications(ctx); err != nil {
		t.Fatal(err)
	}
	if len(transitions) != 4 || transitions[3] != "firing" {
		t.Fatalf("backup failure notification missing: %#v", transitions)
	}

	recoveryStarted := current.Add(time.Minute)
	recoveryFinished := recoveryStarted.Add(time.Minute)
	if err := dataStore.UpsertRun(ctx, domain.RunReport{
		ID: "run_notification_recovery", IdempotencyKey: "notification-recovery", ProjectID: project.ID, ServerID: identity.AgentID,
		ScheduledAt: recoveryStarted, StartedAt: recoveryStarted, FinishedAt: &recoveryFinished, Status: domain.RunSucceeded,
		Stats: map[string]any{"operation": "backup"},
	}); err != nil {
		t.Fatal(err)
	}
	current = recoveryFinished.Add(time.Minute)
	if err := service.EvaluateAlerts(ctx); err != nil {
		t.Fatal(err)
	}
	if err := service.DeliverNotifications(ctx); err != nil {
		t.Fatal(err)
	}
	if len(transitions) != 5 || transitions[4] != "resolved" {
		t.Fatalf("backup recovery notification missing: %#v", transitions)
	}
	incidents, err := dataStore.ListAlertIncidents(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(incidents) != 2 || incidents[0].Status != "resolved" || incidents[1].Status != "resolved" {
		t.Fatalf("unexpected incident history: %#v", incidents)
	}
}

func TestPausingProjectResolvesActiveBackupAlerts(t *testing.T) {
	ctx := context.Background()
	dataStore := memory.New()
	sealer, err := secret.New(bytes.Repeat([]byte{20}, 32))
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(dataStore, sealer)
	enrollment, err := service.CreateServer(ctx, "Paused alert host")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := service.EnrollAgent(ctx, enrollment.EnrollmentToken, domain.AgentInfo{Hostname: "paused-alert-host"})
	if err != nil {
		t.Fatal(err)
	}
	repository, err := service.CreateRepository(ctx, domain.Repository{
		Provider: "local", Name: "Paused alert repository", URL: "/tmp/paused-alert-repository", Password: "password",
	})
	if err != nil {
		t.Fatal(err)
	}
	project, err := service.CreateProject(ctx, domain.Project{
		ServerID: identity.AgentID, RepositoryID: repository.ID, Name: "Paused project",
		Sources:  []domain.Source{{Type: "files", Paths: []string{"/tmp"}, Required: true}},
		Schedule: domain.Schedule{Cron: "0 1 * * *", Timezone: "UTC", MaxRuntimeSeconds: 3600, GraceSeconds: 1800},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	finished := now.Add(time.Minute)
	if err := dataStore.UpsertRun(ctx, domain.RunReport{
		ID: "run_paused_failure", IdempotencyKey: "paused-failure", ProjectID: project.ID, ServerID: identity.AgentID,
		ScheduledAt: now, StartedAt: now, FinishedAt: &finished, Status: domain.RunFailed,
		Stats: map[string]any{"operation": "backup"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.EvaluateAlerts(ctx); err != nil {
		t.Fatal(err)
	}
	incidents, err := dataStore.ListAlertIncidents(ctx, 10)
	if err != nil || len(incidents) != 1 || incidents[0].Status != "firing" {
		t.Fatalf("failed run did not open an incident: %#v err=%v", incidents, err)
	}
	if _, err := service.SetProjectEnabled(ctx, project.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := service.EvaluateAlerts(ctx); err != nil {
		t.Fatal(err)
	}
	incidents, err = dataStore.ListAlertIncidents(ctx, 10)
	if err != nil || len(incidents) != 1 || incidents[0].Status != "resolved" {
		t.Fatalf("pausing project left its incident firing: %#v err=%v", incidents, err)
	}
}

func TestNotificationChannelRejectsUnknownConfigFields(t *testing.T) {
	sealer, err := secret.New(bytes.Repeat([]byte{22}, 32))
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(memory.New(), sealer)
	_, err = service.CreateNotificationChannel(context.Background(), domain.NotificationChannel{
		Name: "Unknown secret field", Type: "webhook", Enabled: true,
		Config: map[string]string{
			"url": "https://alerts.example.com/hook", "api_key": "must-not-be-echoed",
		},
	})
	var validation *ValidationError
	if !errors.As(err, &validation) || validation.Field != "config.api_key" {
		t.Fatalf("unknown notification field was not rejected safely: %v", err)
	}
}

func TestNotificationProviderDefinitionsAreSelfContained(t *testing.T) {
	if len(notificationProviderDefinitions) == 0 {
		t.Fatal("no notification providers are registered")
	}
	for providerType, definition := range notificationProviderDefinitions {
		t.Run(providerType, func(t *testing.T) {
			if definition.Send == nil {
				t.Fatal("provider has no delivery adapter")
			}
			if len(definition.RequiredFields) == 0 {
				t.Fatal("provider has no required fields")
			}
			for _, field := range definition.RequiredFields {
				if _, ok := definition.AllowedFields[field]; !ok {
					t.Fatalf("required field %q is not allowed", field)
				}
			}
			for field := range definition.SecretFields {
				if _, ok := definition.AllowedFields[field]; !ok {
					t.Fatalf("secret field %q is not allowed", field)
				}
			}
		})
	}
}

func TestNotificationDeliveryRetriesAreBoundedAndPersistent(t *testing.T) {
	ctx := context.Background()
	dataStore := memory.New()
	sealer, err := secret.New(bytes.Repeat([]byte{17}, 32))
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(dataStore, sealer)
	current := time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return current }
	channel, err := service.CreateNotificationChannel(ctx, domain.NotificationChannel{
		Name: "Failing webhook", Type: "webhook", Enabled: true, SendResolved: true,
		RepeatIntervalSeconds: 3600, Config: map[string]string{"url": "https://alerts.example.com/private"},
	})
	if err != nil {
		t.Fatal(err)
	}
	alert, err := dataStore.CreateAlertIncident(ctx, domain.AlertIncident{
		ID: "alt_retry", Fingerprint: "backup:retry", Kind: "backup_failure", ProjectID: "prj_retry",
		ProjectName: "Retry project", Status: "firing", Severity: "critical", Summary: "Backup failed",
		Description: "Retry test", SourceEventID: "run_retry", OccurrenceCount: 1, StartedAt: current, UpdatedAt: current,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := dataStore.CreateNotificationDelivery(ctx, domain.NotificationDelivery{
		ID: "ntf_retry", AlertID: alert.ID, ChannelID: channel.ID, Transition: "firing",
		DedupeKey: "retry-delivery", Status: "pending", NextAttemptAt: current, CreatedAt: current,
	}); err != nil {
		t.Fatal(err)
	}
	service.notificationSender = func(context.Context, domain.NotificationChannel, map[string]string, domain.AlertIncident, string) error {
		return errors.New("provider temporarily unavailable")
	}

	expectedBackoff := []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour}
	for attempt := 1; attempt <= maxNotificationAttempts; attempt++ {
		if err := service.DeliverNotifications(ctx); err != nil {
			t.Fatal(err)
		}
		deliveries, err := dataStore.ListNotificationDeliveries(ctx, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(deliveries) != 1 || deliveries[0].AttemptCount != attempt {
			t.Fatalf("attempt %d was not persisted: %#v", attempt, deliveries)
		}
		if attempt == maxNotificationAttempts {
			if deliveries[0].Status != "failed" {
				t.Fatalf("final delivery status = %q, want failed", deliveries[0].Status)
			}
			break
		}
		if deliveries[0].Status != "pending" {
			t.Fatalf("retry delivery status = %q, want pending", deliveries[0].Status)
		}
		wantNext := current.Add(expectedBackoff[attempt-1])
		if !deliveries[0].NextAttemptAt.Equal(wantNext) {
			t.Fatalf("attempt %d next retry = %s, want %s", attempt, deliveries[0].NextAttemptAt, wantNext)
		}
		current = wantNext
	}
}
