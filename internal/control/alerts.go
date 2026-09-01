package control

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/to-alan/vaultmesh/internal/domain"
	"github.com/to-alan/vaultmesh/internal/store"
)

type alertCondition struct {
	Kind, ResourceType, ResourceID, ResourceName, ProjectID, ProjectName string
	SourceEventID, Severity, Summary, Description                        string
}

func (s *Service) EvaluateAlerts(ctx context.Context) error {
	projects, err := s.store.ListProjects(ctx)
	if err != nil {
		return err
	}
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		return err
	}
	healthItems, err := s.ProjectHealth(ctx)
	if err != nil {
		return err
	}
	activityItems, err := s.store.ListProjectBackupActivity(ctx)
	if err != nil {
		return err
	}
	projectNames := make(map[string]string, len(projects))
	for _, project := range projects {
		projectNames[project.ID] = project.Name
	}
	health := make(map[string]domain.ProjectHealth, len(healthItems))
	for _, item := range healthItems {
		health[item.ProjectID] = item
	}
	activity := make(map[string]domain.ProjectBackupActivity, len(activityItems))
	for _, item := range activityItems {
		activity[item.ProjectID] = item
	}
	for _, server := range servers {
		var offline *alertCondition
		if server.Status == domain.ServerOffline && server.LastSeenAt != nil {
			offline = &alertCondition{
				Kind: "agent_offline", ResourceType: "server", ResourceID: server.ID, ResourceName: server.Name,
				SourceEventID: strconv.FormatInt(server.LastSeenAt.Unix(), 10), Severity: "warning",
				Summary: "Agent 已离线",
				Description: fmt.Sprintf("Agent 自 %s 起未再上报心跳，已超过 %s。现有本地计划可能仍在执行，但控制面无法确认状态或下发命令。",
					server.LastSeenAt.Format(time.RFC3339), domain.AgentOfflineAfter),
			}
		}
		if err := s.reconcileAlert(ctx, "agent:"+server.ID, offline); err != nil {
			return err
		}
	}
	for _, project := range projects {
		if !project.Enabled {
			if err := s.reconcileAlert(ctx, "rpo:"+project.ID, nil); err != nil {
				return err
			}
			if err := s.reconcileAlert(ctx, "backup:"+project.ID, nil); err != nil {
				return err
			}
			continue
		}
		healthItem := health[project.ID]
		var rpo *alertCondition
		if healthItem.Status == "overdue" {
			description := "备份完成时限已过，且没有新的成功备份。"
			sourceEventID := project.ID + ":overdue"
			if healthItem.DeadlineAt != nil {
				description = fmt.Sprintf("备份应在 %s 前完成，但控制面仍未看到新的成功记录。", healthItem.DeadlineAt.Format(time.RFC3339))
				sourceEventID = strconv.FormatInt(healthItem.DeadlineAt.Unix(), 10)
			}
			rpo = &alertCondition{Kind: "rpo_overdue", ResourceType: "project", ResourceID: project.ID, ResourceName: project.Name,
				ProjectID: project.ID, ProjectName: project.Name,
				SourceEventID: sourceEventID, Severity: "critical",
				Summary: "备份项目超过 RPO", Description: description}
		}
		if err := s.reconcileAlert(ctx, "rpo:"+project.ID, rpo); err != nil {
			return err
		}
		activityItem := activity[project.ID]
		var failure *alertCondition
		if isBackupFailureStatus(activityItem.LatestRunStatus) {
			failure = &alertCondition{Kind: "backup_failure", ResourceType: "project", ResourceID: project.ID, ResourceName: projectNames[project.ID],
				ProjectID: project.ID, ProjectName: projectNames[project.ID],
				SourceEventID: activityItem.LatestRunID, Severity: backupFailureSeverity(activityItem.LatestRunStatus),
				Summary: "备份运行未成功", Description: fmt.Sprintf("最近一次备份运行状态为 %s。", activityItem.LatestRunStatus)}
		}
		if err := s.reconcileAlert(ctx, "backup:"+project.ID, failure); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) reconcileAlert(ctx context.Context, fingerprint string, condition *alertCondition) error {
	now := s.now()
	current, err := s.store.GetFiringAlertIncident(ctx, fingerprint)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if condition == nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		current.Status = "resolved"
		current.UpdatedAt = now
		current.ResolvedAt = &now
		updated, err := s.store.UpdateAlertIncident(ctx, current)
		if err != nil {
			return err
		}
		return s.enqueueAlertNotifications(ctx, updated, "resolved")
	}
	if errors.Is(err, store.ErrNotFound) {
		id, err := randomValue("alt", 10)
		if err != nil {
			return err
		}
		created, err := s.store.CreateAlertIncident(ctx, domain.AlertIncident{
			ID: id, Fingerprint: fingerprint, Kind: condition.Kind,
			ResourceType: condition.ResourceType, ResourceID: condition.ResourceID, ResourceName: condition.ResourceName,
			ProjectID: condition.ProjectID, ProjectName: condition.ProjectName,
			Status: "firing", Severity: condition.Severity,
			Summary: condition.Summary, Description: condition.Description, SourceEventID: condition.SourceEventID,
			OccurrenceCount: 1, StartedAt: now, UpdatedAt: now,
		})
		if err != nil {
			return err
		}
		return s.enqueueAlertNotifications(ctx, created, "firing")
	}
	changed := false
	if current.SourceEventID != condition.SourceEventID {
		current.SourceEventID = condition.SourceEventID
		current.OccurrenceCount++
		changed = true
	}
	if current.ResourceName != condition.ResourceName || current.ProjectName != condition.ProjectName ||
		current.Severity != condition.Severity || current.Summary != condition.Summary || current.Description != condition.Description {
		current.ResourceName = condition.ResourceName
		current.ProjectName = condition.ProjectName
		current.Severity = condition.Severity
		current.Summary = condition.Summary
		current.Description = condition.Description
		changed = true
	}
	if changed {
		current.UpdatedAt = now
		current, err = s.store.UpdateAlertIncident(ctx, current)
		if err != nil {
			return err
		}
	}
	return s.enqueueAlertNotifications(ctx, current, "repeat")
}

func (s *Service) enqueueAlertNotifications(ctx context.Context, alert domain.AlertIncident, transition string) error {
	channels, err := s.store.ListNotificationChannels(ctx)
	if err != nil {
		return err
	}
	now := s.now()
	for _, channel := range channels {
		if !channel.Enabled || !containsString(channel.EventTypes, alert.Kind) || !matchesAlertScope(channel, alert) {
			continue
		}
		if transition == "resolved" && !channel.SendResolved {
			continue
		}
		if transition == "repeat" {
			interval := time.Duration(channel.RepeatIntervalSeconds) * time.Second
			if now.Sub(alert.StartedAt) < interval {
				continue
			}
		}
		dedupeKey := alert.ID + ":" + channel.ID + ":" + transition
		if transition == "repeat" {
			dedupeKey += ":" + strconv.FormatInt(now.Unix()/int64(channel.RepeatIntervalSeconds), 10)
		}
		id, err := randomValue("ntf", 10)
		if err != nil {
			return err
		}
		err = s.store.CreateNotificationDelivery(ctx, domain.NotificationDelivery{
			ID: id, AlertID: alert.ID, ChannelID: channel.ID, Transition: transition,
			DedupeKey: dedupeKey, Status: "pending", NextAttemptAt: now, CreatedAt: now,
		})
		if err != nil && !errors.Is(err, store.ErrConflict) {
			return err
		}
	}
	return nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func matchesProject(projectIDs []string, projectID string) bool {
	return len(projectIDs) == 0 || containsString(projectIDs, projectID)
}

func matchesAlertScope(channel domain.NotificationChannel, alert domain.AlertIncident) bool {
	if alert.ResourceType == "server" {
		return len(channel.ServerIDs) == 0 || containsString(channel.ServerIDs, alert.ResourceID)
	}
	return matchesProject(channel.ProjectIDs, alert.ProjectID)
}

func isBackupFailureStatus(status string) bool {
	return status == domain.RunPartial || status == domain.RunFailed || status == domain.RunTimedOut ||
		status == domain.RunUnknown || status == domain.RunCanceled
}

func backupFailureSeverity(status string) string {
	if status == domain.RunPartial {
		return "warning"
	}
	return "critical"
}

func (s *Service) DeliverNotifications(ctx context.Context) error {
	now := s.now()
	deliveries, err := s.store.ClaimNotificationDeliveries(ctx, now, now.Add(notificationLease), 20)
	if err != nil {
		return err
	}
	for _, delivery := range deliveries {
		channel, channelErr := s.store.GetNotificationChannel(ctx, delivery.ChannelID)
		alert, alertErr := s.store.GetAlertIncident(ctx, delivery.AlertID)
		var sendErr error
		if channelErr != nil {
			sendErr = channelErr
		} else if alertErr != nil {
			sendErr = alertErr
		} else if channel.DeletedAt != nil || !channel.Enabled {
			sendErr = errors.New("notification channel is disabled or archived")
		} else {
			config, err := s.openNotificationConfig(channel)
			if err != nil {
				sendErr = err
			} else {
				sendErr = s.notificationSender(ctx, channel, config, alert, delivery.Transition)
			}
		}
		if sendErr == nil {
			if err := s.store.CompleteNotificationDelivery(ctx, delivery.ID, true, "", now, time.Time{}); err != nil {
				return err
			}
			continue
		}
		lastError := boundedNotificationError(sendErr)
		var next time.Time
		if delivery.AttemptCount < maxNotificationAttempts && channelErr == nil && alertErr == nil && channel.DeletedAt == nil && channel.Enabled {
			backoff := []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour}[min(delivery.AttemptCount-1, 3)]
			next = now.Add(backoff)
		}
		if err := s.store.CompleteNotificationDelivery(ctx, delivery.ID, false, lastError, now, next); err != nil {
			return err
		}
	}
	return nil
}

func boundedNotificationError(err error) string {
	value := strings.TrimSpace(err.Error())
	if len(value) > 500 {
		value = value[:500]
	}
	return value
}

func (s *Service) PruneExpired(ctx context.Context) (int64, error) {
	retention := s.dataRetention
	if !retention.Enabled() {
		return 0, nil
	}
	now := s.now()
	var total int64
	scopes := []struct {
		scope store.RetentionScope
		days  int
	}{
		{store.RetentionRuns, retention.RunsDays},
		{store.RetentionCommands, retention.CommandsDays},
		{store.RetentionDeliveries, retention.DeliveriesDays},
		{store.RetentionIncidents, retention.IncidentsDays},
		{store.RetentionAudit, retention.AuditEventsDays},
	}
	for _, item := range scopes {
		if item.days <= 0 {
			continue
		}
		removed, err := s.store.PruneBefore(ctx, item.scope, now.Add(-time.Duration(item.days)*24*time.Hour))
		if err != nil {
			return total, err
		}
		total += removed
	}
	return total, nil
}

func (s *Service) RunNotificationWorker(ctx context.Context, logger *slog.Logger) {
	run := func() {
		cycleCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		if err := s.EvaluateAlerts(cycleCtx); err != nil {
			logger.Error("evaluate notification alerts", "error", err)
			return
		}
		if err := s.DeliverNotifications(cycleCtx); err != nil {
			logger.Error("deliver notifications", "error", err)
		}
	}
	prune := func() {
		cycleCtx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		removed, err := s.PruneExpired(cycleCtx)
		if err != nil {
			logger.Error("prune expired control-plane records", "error", err)
			return
		}
		if removed > 0 {
			logger.Info("pruned expired control-plane records", "removed", removed)
		}
	}
	run()
	prune()
	pruneTicker := time.NewTicker(retentionInterval)
	defer pruneTicker.Stop()
	ticker := time.NewTicker(notificationWorkerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		case <-pruneTicker.C:
			prune()
		}
	}
}
