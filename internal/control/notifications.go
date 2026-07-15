package control

import (
	"context"
	"encoding/json"
	"fmt"
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

type notificationProviderDefinition struct {
	RequiredFields []string
	AllowedFields  map[string]struct{}
	SecretFields   map[string]struct{}
	Send           notificationProviderSend
}

var notificationProviderDefinitions = map[string]notificationProviderDefinition{
	"webhook": notificationProvider(
		[]string{"url"},
		[]string{"url", "method", "authorization", "headers", "body_template", "allow_private_address"},
		[]string{"url", "headers", "authorization", "body_template"},
		sendWebhookProvider,
	),
	"telegram": notificationProvider(
		[]string{"bot_token", "chat_id"},
		[]string{"bot_token", "chat_id", "message_thread_id", "allow_private_address"},
		[]string{"bot_token"},
		sendTelegramProvider,
	),
	"email": notificationProvider(
		[]string{"smtp_host", "smtp_port", "from", "to"},
		[]string{"smtp_host", "smtp_port", "security", "username", "password", "from", "to", "allow_private_address"},
		[]string{"password"},
		sendEmailProvider,
	),
	"slack": notificationProvider(
		[]string{"webhook_url"}, []string{"webhook_url", "allow_private_address"}, []string{"webhook_url"}, sendSlackProvider,
	),
	"discord": notificationProvider(
		[]string{"webhook_url"}, []string{"webhook_url", "allow_private_address"}, []string{"webhook_url"}, sendDiscordProvider,
	),
	"gotify": notificationProvider(
		[]string{"server_url", "token"},
		[]string{"server_url", "token", "priority", "allow_private_address"},
		[]string{"token"},
		sendGotifyProvider,
	),
	"ntfy": notificationProvider(
		[]string{"server_url", "topic"},
		[]string{"server_url", "topic", "token", "allow_private_address"},
		[]string{"token"},
		sendNtfyProvider,
	),
	"wecom": notificationProvider(
		[]string{"webhook_url"}, []string{"webhook_url", "allow_private_address"}, []string{"webhook_url"}, sendWeComProvider,
	),
	"dingtalk": notificationProvider(
		[]string{"webhook_url"}, []string{"webhook_url", "allow_private_address"}, []string{"webhook_url"}, sendDingTalkProvider,
	),
}

var notificationEventTypes = map[string]struct{}{
	"backup_failure": {},
	"rpo_overdue":    {},
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
	provider, ok := notificationProviderDefinitions[input.Type]
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
	for _, key := range provider.RequiredFields {
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
	provider, ok := notificationProviderDefinitions[channelType]
	if !ok {
		return validationError("type", "is not a supported notification channel")
	}
	for key := range config {
		if _, allowed := provider.AllowedFields[key]; !allowed {
			return validationError("config."+key, "is not supported for this notification channel")
		}
	}
	if value := config["allow_private_address"]; value != "" {
		allowed, err := strconv.ParseBool(value)
		if err != nil {
			return validationError("config.allow_private_address", "must be true or false")
		}
		config["allow_private_address"] = strconv.FormatBool(allowed)
	} else {
		config["allow_private_address"] = "false"
	}
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
			managedHeaders := map[string]struct{}{
				"host": {}, "content-length": {}, "connection": {}, "proxy-authorization": {},
				"te": {}, "trailer": {}, "transfer-encoding": {}, "upgrade": {},
			}
			for key := range values {
				if _, managed := managedHeaders[strings.ToLower(strings.TrimSpace(key))]; managed {
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
		provider, knownProvider := notificationProviderDefinitions[channel.Type]
		for key, value := range config {
			_, allowed := provider.AllowedFields[key]
			if _, secret := provider.SecretFields[key]; knownProvider && allowed && !secret {
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

func fieldSet(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func notificationProvider(required, allowed, secret []string, sender notificationProviderSend) notificationProviderDefinition {
	return notificationProviderDefinition{
		RequiredFields: required,
		AllowedFields:  fieldSet(allowed...),
		SecretFields:   fieldSet(secret...),
		Send:           sender,
	}
}
