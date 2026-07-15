package control

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/to-alan/vaultmesh/internal/domain"
)

var newNotificationHTTPClient = func() *http.Client {
	return &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func sendNotification(ctx context.Context, channel domain.NotificationChannel, config map[string]string, alert domain.AlertIncident, transition string) error {
	title, message := notificationMessage(alert, transition)
	switch channel.Type {
	case "email":
		return sendEmailNotification(ctx, config, title, message)
	case "telegram":
		endpoint := "https://api.telegram.org/bot" + url.PathEscape(config["bot_token"]) + "/sendMessage"
		payload := map[string]any{"chat_id": config["chat_id"], "text": title + "\n\n" + message}
		if threadID := config["message_thread_id"]; threadID != "" {
			payload["message_thread_id"], _ = strconv.ParseInt(threadID, 10, 64)
		}
		return postNotificationJSON(ctx, endpoint, nil, payload)
	case "slack":
		return postNotificationJSON(ctx, config["webhook_url"], nil, map[string]any{"text": "*" + title + "*\n" + message})
	case "discord":
		return postNotificationJSON(ctx, config["webhook_url"], nil, map[string]any{"content": "**" + title + "**\n" + message})
	case "wecom":
		return postNotificationJSON(ctx, config["webhook_url"], nil, map[string]any{"msgtype": "markdown", "markdown": map[string]string{"content": "**" + title + "**\n> " + strings.ReplaceAll(message, "\n", "\n> ")}})
	case "dingtalk":
		return postNotificationJSON(ctx, config["webhook_url"], nil, map[string]any{"msgtype": "markdown", "markdown": map[string]string{"title": title, "text": "### " + title + "\n\n" + message}})
	case "gotify":
		endpoint := strings.TrimRight(config["server_url"], "/") + "/message?token=" + url.QueryEscape(config["token"])
		priority, _ := strconv.Atoi(config["priority"])
		return postNotificationJSON(ctx, endpoint, nil, map[string]any{"title": title, "message": message, "priority": priority})
	case "ntfy":
		endpoint := strings.TrimRight(config["server_url"], "/") + "/" + url.PathEscape(config["topic"])
		headers := map[string]string{"Title": title, "Priority": severityPriority(alert.Severity)}
		if token := config["token"]; token != "" {
			headers["Authorization"] = "Bearer " + token
		}
		return sendNotificationHTTP(ctx, http.MethodPost, endpoint, headers, []byte(message), "text/plain; charset=utf-8")
	case "webhook":
		return sendGenericWebhook(ctx, config, alert, transition, title, message)
	default:
		return fmt.Errorf("unsupported notification channel type %q", channel.Type)
	}
}

func notificationMessage(alert domain.AlertIncident, transition string) (string, string) {
	state := "告警"
	if transition == "repeat" {
		state = "持续告警"
	} else if transition == "resolved" {
		state = "已恢复"
	}
	title := fmt.Sprintf("[VaultMesh] %s · %s", state, alert.ProjectName)
	message := strings.Join([]string{
		"事件：" + alert.Summary,
		"项目：" + alert.ProjectName,
		"级别：" + alert.Severity,
		"状态：" + transition,
		"说明：" + alert.Description,
		fmt.Sprintf("发生次数：%d", alert.OccurrenceCount),
		"时间：" + alert.UpdatedAt.Format(time.RFC3339),
	}, "\n")
	return title, message
}

func severityPriority(severity string) string {
	if severity == "critical" {
		return "high"
	}
	if severity == "warning" {
		return "default"
	}
	return "low"
}

func sendGenericWebhook(ctx context.Context, config map[string]string, alert domain.AlertIncident, transition, title, message string) error {
	payload := map[string]any{
		"version": "1", "transition": transition, "title": title, "message": message,
		"alert": alert,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if template := config["body_template"]; template != "" {
		replacements := map[string]string{
			"{{transition}}": transition, "{{title}}": title, "{{message}}": message,
			"{{project_name}}": alert.ProjectName, "{{severity}}": alert.Severity,
			"{{summary}}": alert.Summary, "{{description}}": alert.Description,
		}
		for placeholder, value := range replacements {
			template = strings.ReplaceAll(template, placeholder, value)
		}
		body = []byte(template)
	}
	headers := map[string]string{}
	if raw := config["headers"]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &headers); err != nil {
			return errors.New("decode configured webhook headers")
		}
	}
	if authorization := config["authorization"]; authorization != "" {
		headers["Authorization"] = authorization
	}
	return sendNotificationHTTP(ctx, config["method"], config["url"], headers, body, "application/json")
}

func postNotificationJSON(ctx context.Context, endpoint string, headers map[string]string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return sendNotificationHTTP(ctx, http.MethodPost, endpoint, headers, body, "application/json")
}

func sendNotificationHTTP(ctx context.Context, method, endpoint string, headers map[string]string, body []byte, contentType string) error {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New("build notification request")
	}
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("User-Agent", "VaultMesh-Notification/1")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	client := newNotificationHTTPClient()
	response, err := client.Do(request)
	if err != nil {
		return errors.New("notification endpoint request failed")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("notification endpoint returned HTTP %d", response.StatusCode)
	}
	return nil
}

func sendEmailNotification(ctx context.Context, config map[string]string, title, message string) error {
	host := config["smtp_host"]
	address := net.JoinHostPort(host, config["smtp_port"])
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	var connection net.Conn
	var err error
	if config["security"] == "tls" {
		connection, err = tls.DialWithDialer(dialer, "tcp", address, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	} else {
		connection, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return errors.New("connect to SMTP server")
	}
	defer connection.Close()
	client, err := smtp.NewClient(connection, host)
	if err != nil {
		return errors.New("initialize SMTP session")
	}
	defer client.Close()
	if config["security"] == "starttls" {
		if err := client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			return errors.New("start SMTP TLS")
		}
	}
	if username := config["username"]; username != "" {
		if err := client.Auth(smtp.PlainAuth("", username, config["password"], host)); err != nil {
			return errors.New("authenticate to SMTP server")
		}
	}
	fromAddress, _ := mail.ParseAddress(config["from"])
	if err := client.Mail(fromAddress.Address); err != nil {
		return errors.New("set SMTP sender")
	}
	var recipients []string
	for _, value := range strings.Split(config["to"], ",") {
		address, _ := mail.ParseAddress(strings.TrimSpace(value))
		recipients = append(recipients, address.Address)
		if err := client.Rcpt(address.Address); err != nil {
			return errors.New("set SMTP recipient")
		}
	}
	writer, err := client.Data()
	if err != nil {
		return errors.New("open SMTP message")
	}
	safeTitle := strings.NewReplacer("\r", " ", "\n", " ").Replace(title)
	headers := "From: " + fromAddress.String() + "\r\n" +
		"To: " + strings.Join(recipients, ", ") + "\r\n" +
		"Subject: " + mime.QEncoding.Encode("UTF-8", safeTitle) + "\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n"
	if _, err := writer.Write([]byte(headers + message)); err != nil {
		return errors.New("write SMTP message")
	}
	if err := writer.Close(); err != nil {
		return errors.New("finish SMTP message")
	}
	return client.Quit()
}
