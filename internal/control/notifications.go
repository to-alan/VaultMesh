package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/to-alan/vaultmesh/internal/domain"
	"github.com/to-alan/vaultmesh/internal/store"
)

const (
	defaultNotificationRepeat  = 4 * time.Hour
	notificationWorkerInterval = 30 * time.Second
	notificationLease          = time.Minute
	maxNotificationAttempts    = 5
)

var notificationChannelTypes = map[string][]string{
	"webhook":  {"url"},
	"telegram": {"bot_token", "chat_id"},
	"email":    {"smtp_host", "smtp_port", "from", "to"},
	"slack":    {"webhook_url"},
	"discord":  {"webhook_url"},
	"gotify":   {"server_url", "token"},
	"ntfy":     {"server_url", "topic"},
	"wecom":    {"webhook_url"},
	"dingtalk": {"webhook_url"},
}

var notificationEventTypes = map[string]struct{}{
	"backup_failure": {},
	"rpo_overdue":    {},
}

var notificationSecretFields = map[string]map[string]struct{}{
	"webhook":  {"url": {}, "headers": {}, "authorization": {}, "body_template": {}},
	"telegram": {"bot_token": {}},
	"email":    {"password": {}},
	"slack":    {"webhook_url": {}},
	"discord":  {"webhook_url": {}},
	"gotify":   {"token": {}},
	"ntfy":     {"token": {}},
	"wecom":    {"webhook_url": {}},
	"dingtalk": {"webhook_url": {}},
}

type notificationSender func(context.Context, domain.NotificationChannel, map[string]string, domain.AlertIncident, string) error

func (s *Service) CreateNotificationChannel(ctx context.Context, input domain.NotificationChannel) (domain.NotificationChannel, error) {
	now := s.now()
	input.ID = ""
	input.CreatedAt = now
	input.UpdatedAt = now
	if input.RepeatIntervalSeconds == 0 {
		input.RepeatIntervalSeconds = int(defaultNotificationRepeat.Seconds())
	}
	if err := s.prepareNotificationChannel(ctx, &input, nil); err != nil {
		return domain.NotificationChannel{}, err
	}
	id, err := randomValue("chn", 10)
	if err != nil {
		return domain.NotificationChannel{}, err
	}
	input.ID = id
	created, err := s.store.CreateNotificationChannel(ctx, input)
	if err != nil {
		return domain.NotificationChannel{}, err
	}
	return s.publicNotificationChannel(created), nil
}

func (s *Service) UpdateNotificationChannel(ctx context.Context, id string, input domain.NotificationChannel) (domain.NotificationChannel, error) {
	current, err := s.store.GetNotificationChannel(ctx, strings.TrimSpace(id))
	if err != nil {
		return domain.NotificationChannel{}, err
	}
	if current.DeletedAt != nil {
		return domain.NotificationChannel{}, store.ErrNotFound
	}
	input.ID = current.ID
	input.CreatedAt = current.CreatedAt
	input.UpdatedAt = s.now()
	if err := s.prepareNotificationChannel(ctx, &input, &current); err != nil {
		return domain.NotificationChannel{}, err
	}
	updated, err := s.store.UpdateNotificationChannel(ctx, input)
	if err != nil {
		return domain.NotificationChannel{}, err
	}
	return s.publicNotificationChannel(updated), nil
}

func (s *Service) SetNotificationChannelEnabled(ctx context.Context, id string, enabled bool) (domain.NotificationChannel, error) {
	current, err := s.store.GetNotificationChannel(ctx, id)
	if err != nil {
		return domain.NotificationChannel{}, err
	}
	current.Enabled = enabled
	current.UpdatedAt = s.now()
	updated, err := s.store.UpdateNotificationChannel(ctx, current)
	if err != nil {
		return domain.NotificationChannel{}, err
	}
	return s.publicNotificationChannel(updated), nil
}

func (s *Service) ListNotificationChannels(ctx context.Context) ([]domain.NotificationChannel, error) {
	items, err := s.store.ListNotificationChannels(ctx)
	if err != nil {
		return nil, err
	}
	for index := range items {
		items[index] = s.publicNotificationChannel(items[index])
	}
	return items, nil
}

func (s *Service) TestNotificationChannel(ctx context.Context, id string) error {
	channel, err := s.store.GetNotificationChannel(ctx, id)
	if err != nil {
		return err
	}
	config, err := s.openNotificationConfig(channel)
	if err != nil {
		return err
	}
	now := s.now()
	return s.notificationSender(ctx, channel, config, domain.AlertIncident{
		ID: "test", Kind: "test", ProjectName: "VaultMesh 通知测试", Status: "firing",
		Severity: "info", Summary: "通知渠道连接成功", Description: "这是一条由管理员主动发送的测试通知。",
		StartedAt: now, UpdatedAt: now, OccurrenceCount: 1,
	}, "firing")
}

func (s *Service) prepareNotificationChannel(ctx context.Context, input *domain.NotificationChannel, current *domain.NotificationChannel) error {
	input.Name = strings.TrimSpace(input.Name)
	input.Type = strings.TrimSpace(input.Type)
	if input.Name == "" || len(input.Name) > 100 {
		return validationError("name", "must contain 1 to 100 characters")
	}
	required, ok := notificationChannelTypes[input.Type]
	if !ok {
		return validationError("type", "is not a supported notification channel")
	}
	if input.RepeatIntervalSeconds < 300 || input.RepeatIntervalSeconds > 7*24*60*60 {
		return validationError("repeat_interval_seconds", "must be between 5 minutes and 7 days")
	}
	if len(input.EventTypes) == 0 {
		input.EventTypes = []string{"backup_failure", "rpo_overdue"}
	}
	input.EventTypes = uniqueStrings(input.EventTypes)
	for _, eventType := range input.EventTypes {
		if _, ok := notificationEventTypes[eventType]; !ok {
			return validationError("event_types", fmt.Sprintf("unsupported event type %q", eventType))
		}
	}
	input.ProjectIDs = uniqueStrings(input.ProjectIDs)
	if len(input.ProjectIDs) > 0 {
		projects, err := s.store.ListProjects(ctx)
		if err != nil {
			return err
		}
		known := make(map[string]struct{}, len(projects))
		for _, project := range projects {
			known[project.ID] = struct{}{}
		}
		for _, id := range input.ProjectIDs {
			if _, ok := known[id]; !ok {
				return validationError("project_ids", fmt.Sprintf("project %q does not exist", id))
			}
		}
	}
	config := map[string]string{}
	if current != nil && current.Type == input.Type {
		existing, err := s.openNotificationConfig(*current)
		if err != nil {
			return err
		}
		config = existing
	}
	for key, value := range input.Config {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			config[key] = trimmed
		}
	}
	for _, key := range required {
		if config[key] == "" {
			return validationError("config."+key, "is required")
		}
	}
	if err := validateNotificationConfig(input.Type, config); err != nil {
		return err
	}
	payload, err := json.Marshal(config)
	if err != nil {
		return err
	}
	sealed, err := s.sealer.Seal(payload)
	if err != nil {
		return err
	}
	input.SecretCiphertext = sealed
	input.Config = nil
	input.Configured = true
	return nil
}

func validateNotificationConfig(channelType string, config map[string]string) error {
	for _, key := range []string{"url", "webhook_url", "server_url"} {
		if value := config[key]; value != "" {
			parsed, err := url.Parse(value)
			if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.User != nil {
				return validationError("config."+key, "must be an absolute HTTP or HTTPS URL without embedded credentials")
			}
		}
	}
	if channelType == "webhook" {
		method := strings.ToUpper(config["method"])
		if method == "" {
			method = "POST"
		}
		if method != "POST" && method != "PUT" {
			return validationError("config.method", "must be POST or PUT")
		}
		config["method"] = method
		if headers := config["headers"]; headers != "" {
			var values map[string]string
			if json.Unmarshal([]byte(headers), &values) != nil {
				return validationError("config.headers", "must be a JSON object of string headers")
			}
			for key := range values {
				if strings.EqualFold(key, "Host") || strings.EqualFold(key, "Content-Length") {
					return validationError("config.headers", fmt.Sprintf("header %q is managed by VaultMesh", key))
				}
			}
		}
	}
	if channelType == "email" {
		port, err := strconv.Atoi(config["smtp_port"])
		if err != nil || port < 1 || port > 65535 {
			return validationError("config.smtp_port", "must be a valid TCP port")
		}
		security := config["security"]
		if security == "" {
			security = "starttls"
		}
		if security != "starttls" && security != "tls" && security != "none" {
			return validationError("config.security", "must be starttls, tls, or none")
		}
		config["security"] = security
		if _, err := mail.ParseAddress(config["from"]); err != nil {
			return validationError("config.from", "must be a valid email address")
		}
		for _, recipient := range strings.Split(config["to"], ",") {
			if _, err := mail.ParseAddress(strings.TrimSpace(recipient)); err != nil {
				return validationError("config.to", "must contain comma-separated email addresses")
			}
		}
	}
	return nil
}

func (s *Service) publicNotificationChannel(channel domain.NotificationChannel) domain.NotificationChannel {
	config, err := s.openNotificationConfig(channel)
	if err == nil {
		channel.Configured = true
		channel.Destination = notificationDestination(channel.Type, config)
		channel.Config = make(map[string]string)
		secrets := notificationSecretFields[channel.Type]
		for key, value := range config {
			if _, secret := secrets[key]; !secret {
				channel.Config[key] = value
			}
		}
	}
	channel.SecretCiphertext = nil
	return channel
}

func (s *Service) openNotificationConfig(channel domain.NotificationChannel) (map[string]string, error) {
	plaintext, err := s.sealer.Open(channel.SecretCiphertext)
	if err != nil {
		return nil, fmt.Errorf("decrypt notification channel %s: %w", channel.ID, err)
	}
	var config map[string]string
	if err := json.Unmarshal(plaintext, &config); err != nil {
		return nil, fmt.Errorf("decode notification channel %s: %w", channel.ID, err)
	}
	return config, nil
}

func notificationDestination(channelType string, config map[string]string) string {
	if channelType == "telegram" {
		return "Chat " + config["chat_id"]
	}
	if channelType == "email" {
		return config["to"]
	}
	if channelType == "ntfy" {
		return hostOf(config["server_url"]) + "/" + config["topic"]
	}
	for _, key := range []string{"url", "webhook_url", "server_url"} {
		if config[key] != "" {
			return hostOf(config[key])
		}
	}
	return channelType
}

func hostOf(value string) string {
	parsed, _ := url.Parse(value)
	if parsed != nil && parsed.Host != "" {
		return parsed.Host
	}
	return "已配置"
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

type alertCondition struct {
	Kind, ProjectID, ProjectName, SourceEventID, Severity, Summary, Description string
}

func (s *Service) EvaluateAlerts(ctx context.Context) error {
	projects, err := s.store.ListProjects(ctx)
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
	for _, project := range projects {
		healthItem := health[project.ID]
		var rpo *alertCondition
		if healthItem.Status == "overdue" {
			description := "备份完成时限已过，且没有新的成功备份。"
			sourceEventID := project.ID + ":overdue"
			if healthItem.DeadlineAt != nil {
				description = fmt.Sprintf("备份应在 %s 前完成，但控制面仍未看到新的成功记录。", healthItem.DeadlineAt.Format(time.RFC3339))
				sourceEventID = strconv.FormatInt(healthItem.DeadlineAt.Unix(), 10)
			}
			rpo = &alertCondition{Kind: "rpo_overdue", ProjectID: project.ID, ProjectName: project.Name,
				SourceEventID: sourceEventID, Severity: "critical",
				Summary: "备份项目超过 RPO", Description: description}
		}
		if err := s.reconcileAlert(ctx, "rpo:"+project.ID, rpo); err != nil {
			return err
		}
		activityItem := activity[project.ID]
		var failure *alertCondition
		if isBackupFailureStatus(activityItem.LatestRunStatus) {
			failure = &alertCondition{Kind: "backup_failure", ProjectID: project.ID, ProjectName: projectNames[project.ID],
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
			ID: id, Fingerprint: fingerprint, Kind: condition.Kind, ProjectID: condition.ProjectID,
			ProjectName: condition.ProjectName, Status: "firing", Severity: condition.Severity,
			Summary: condition.Summary, Description: condition.Description, SourceEventID: condition.SourceEventID,
			OccurrenceCount: 1, StartedAt: now, UpdatedAt: now,
		})
		if err != nil {
			return err
		}
		return s.enqueueAlertNotifications(ctx, created, "firing")
	}
	if current.SourceEventID != condition.SourceEventID {
		current.SourceEventID = condition.SourceEventID
		current.OccurrenceCount++
		current.UpdatedAt = now
		current.Severity = condition.Severity
		current.Description = condition.Description
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
		if !channel.Enabled || !containsString(channel.EventTypes, alert.Kind) || !matchesProject(channel.ProjectIDs, alert.ProjectID) {
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
	run()
	ticker := time.NewTicker(notificationWorkerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}
