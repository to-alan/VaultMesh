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
	"net/netip"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/to-alan/vaultmesh/internal/domain"
)

var newNotificationHTTPClient = func(allowPrivate bool) *http.Client {
	return &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           notificationDialContext(allowPrivate),
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          20,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
		},
	}
}

var alwaysRestrictedNotificationPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("100.100.100.200/32"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
	netip.MustParsePrefix("fd00:ec2::254/128"),
}

var privateNotificationPrefixes = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("fc00::/7"),
}

type notificationProviderSend func(context.Context, map[string]string, domain.AlertIncident, string, string, string, bool) error

func sendNotification(ctx context.Context, channel domain.NotificationChannel, config map[string]string, alert domain.AlertIncident, transition string) error {
	title, message := notificationMessage(alert, transition)
	allowPrivate := config["allow_private_address"] == "true"
	provider, ok := notificationProviderDefinitions[channel.Type]
	if !ok || provider.Send == nil {
		return fmt.Errorf("unsupported notification channel type %q", channel.Type)
	}
	return provider.Send(ctx, config, alert, transition, title, message, allowPrivate)
}

func sendEmailProvider(ctx context.Context, config map[string]string, _ domain.AlertIncident, _, title, message string, allowPrivate bool) error {
	return sendEmailNotification(ctx, config, title, message, allowPrivate)
}

func sendTelegramProvider(ctx context.Context, config map[string]string, _ domain.AlertIncident, _, title, message string, _ bool) error {
	endpoint := "https://api.telegram.org/bot" + url.PathEscape(config["bot_token"]) + "/sendMessage"
	payload := map[string]any{"chat_id": config["chat_id"], "text": title + "\n\n" + message}
	if threadID := config["message_thread_id"]; threadID != "" {
		payload["message_thread_id"], _ = strconv.ParseInt(threadID, 10, 64)
	}
	return postNotificationJSON(ctx, endpoint, nil, payload, false)
}

func sendSlackProvider(ctx context.Context, config map[string]string, _ domain.AlertIncident, _, title, message string, allowPrivate bool) error {
	return postNotificationJSON(ctx, config["webhook_url"], nil, map[string]any{"text": "*" + title + "*\n" + message}, allowPrivate)
}

func sendDiscordProvider(ctx context.Context, config map[string]string, _ domain.AlertIncident, _, title, message string, allowPrivate bool) error {
	return postNotificationJSON(ctx, config["webhook_url"], nil, map[string]any{"content": "**" + title + "**\n" + message}, allowPrivate)
}

func sendWeComProvider(ctx context.Context, config map[string]string, _ domain.AlertIncident, _, title, message string, allowPrivate bool) error {
	payload := map[string]any{"msgtype": "markdown", "markdown": map[string]string{"content": "**" + title + "**\n> " + strings.ReplaceAll(message, "\n", "\n> ")}}
	return postNotificationJSON(ctx, config["webhook_url"], nil, payload, allowPrivate)
}

func sendDingTalkProvider(ctx context.Context, config map[string]string, _ domain.AlertIncident, _, title, message string, allowPrivate bool) error {
	payload := map[string]any{"msgtype": "markdown", "markdown": map[string]string{"title": title, "text": "### " + title + "\n\n" + message}}
	return postNotificationJSON(ctx, config["webhook_url"], nil, payload, allowPrivate)
}

func sendGotifyProvider(ctx context.Context, config map[string]string, _ domain.AlertIncident, _, title, message string, allowPrivate bool) error {
	endpoint := strings.TrimRight(config["server_url"], "/") + "/message?token=" + url.QueryEscape(config["token"])
	priority, _ := strconv.Atoi(config["priority"])
	return postNotificationJSON(ctx, endpoint, nil, map[string]any{"title": title, "message": message, "priority": priority}, allowPrivate)
}

func sendNtfyProvider(ctx context.Context, config map[string]string, alert domain.AlertIncident, _, title, message string, allowPrivate bool) error {
	endpoint := strings.TrimRight(config["server_url"], "/") + "/" + url.PathEscape(config["topic"])
	headers := map[string]string{"Title": title, "Priority": severityPriority(alert.Severity)}
	if token := config["token"]; token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	return sendNotificationHTTP(ctx, http.MethodPost, endpoint, headers, []byte(message), "text/plain; charset=utf-8", allowPrivate)
}

func sendWebhookProvider(ctx context.Context, config map[string]string, alert domain.AlertIncident, transition, title, message string, _ bool) error {
	return sendGenericWebhook(ctx, config, alert, transition, title, message)
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
	return sendNotificationHTTP(ctx, config["method"], config["url"], headers, body, "application/json", config["allow_private_address"] == "true")
}

func postNotificationJSON(ctx context.Context, endpoint string, headers map[string]string, payload any, allowPrivate bool) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return sendNotificationHTTP(ctx, http.MethodPost, endpoint, headers, body, "application/json", allowPrivate)
}

func sendNotificationHTTP(ctx context.Context, method, endpoint string, headers map[string]string, body []byte, contentType string, allowPrivate bool) error {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New("build notification request")
	}
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("User-Agent", "VaultMesh-Notification/1")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	client := newNotificationHTTPClient(allowPrivate)
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

func sendEmailNotification(ctx context.Context, config map[string]string, title, message string, allowPrivate bool) error {
	host := config["smtp_host"]
	connection, err := dialNotificationEndpoint(ctx, "tcp", net.JoinHostPort(host, config["smtp_port"]), allowPrivate)
	if err != nil {
		return errors.New("connect to SMTP server")
	}
	if config["security"] == "tls" {
		tlsConnection := tls.Client(connection, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
		if err := tlsConnection.HandshakeContext(ctx); err != nil {
			connection.Close()
			return errors.New("start SMTP TLS")
		}
		connection = tlsConnection
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

func notificationDialContext(allowPrivate bool) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		return dialNotificationEndpoint(ctx, network, address, allowPrivate)
	}
}

func dialNotificationEndpoint(ctx context.Context, network, address string, allowPrivate bool) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, errors.New("invalid notification endpoint address")
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(addresses) == 0 {
		return nil, errors.New("resolve notification endpoint")
	}
	for _, address := range addresses {
		if !notificationAddressAllowed(address.IP, allowPrivate) {
			return nil, errors.New("notification endpoint resolves to a restricted address")
		}
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	var lastErr error
	for _, address := range addresses {
		connection, err := dialer.DialContext(ctx, network, net.JoinHostPort(address.IP.String(), port))
		if err == nil {
			return connection, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("connect to notification endpoint")
}

func notificationAddressAllowed(address net.IP, allowPrivate bool) bool {
	parsed, ok := netip.AddrFromSlice(address)
	if !ok {
		return false
	}
	parsed = parsed.Unmap()
	for _, prefix := range alwaysRestrictedNotificationPrefixes {
		if prefix.Contains(parsed) {
			return false
		}
	}
	for _, prefix := range privateNotificationPrefixes {
		if prefix.Contains(parsed) {
			return allowPrivate
		}
	}
	return parsed.IsGlobalUnicast()
}
