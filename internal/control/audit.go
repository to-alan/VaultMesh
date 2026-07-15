package control

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/to-alan/vaultmesh/internal/domain"
)

const (
	publicAuditFailureSampleWindow = time.Minute
	publicAuditFailureMaxEntries   = 2048
)

type auditSpec struct {
	Action               string
	Actor                string
	ResourceType         string
	ResourceID           string
	PathValue            string
	SkipAnonymousSuccess bool
}

type auditFailureSampler struct {
	mu       sync.Mutex
	lastSeen map[string]time.Time
}

func newAuditFailureSampler() *auditFailureSampler {
	return &auditFailureSampler{lastSeen: make(map[string]time.Time)}
}

func (s *auditFailureSampler) allow(action, clientIP string, status int, now time.Time) bool {
	key := action + "\x00" + clientIP + "\x00" + strconv.Itoa(status)
	s.mu.Lock()
	defer s.mu.Unlock()
	if last, exists := s.lastSeen[key]; exists && now.Sub(last) < publicAuditFailureSampleWindow {
		return false
	}
	if _, exists := s.lastSeen[key]; !exists && len(s.lastSeen) >= publicAuditFailureMaxEntries {
		var oldestKey string
		var oldestTime time.Time
		for candidate, last := range s.lastSeen {
			if oldestKey == "" || last.Before(oldestTime) {
				oldestKey = candidate
				oldestTime = last
			}
		}
		delete(s.lastSeen, oldestKey)
	}
	s.lastSeen[key] = now
	return true
}

var adminAuditSpecs = map[string]auditSpec{
	"POST /api/v1/profile/reauthenticate":                              {Action: "security.reauthenticate", ResourceType: "account"},
	"POST /api/v1/profile/password":                                    {Action: "security.password.change", ResourceType: "account"},
	"POST /api/v1/profile/totp/begin":                                  {Action: "security.totp.setup.begin", ResourceType: "account"},
	"POST /api/v1/profile/totp/enable":                                 {Action: "security.totp.enable", ResourceType: "account"},
	"POST /api/v1/profile/totp/disable":                                {Action: "security.totp.disable", ResourceType: "account"},
	"POST /api/v1/profile/recovery-codes":                              {Action: "security.recovery_codes.regenerate", ResourceType: "account"},
	"POST /api/v1/profile/passkeys/register/begin":                     {Action: "security.passkey.register.begin", ResourceType: "account"},
	"POST /api/v1/profile/passkeys/register/finish":                    {Action: "security.passkey.register", ResourceType: "account"},
	"POST /api/v1/profile/passkeys/{passkeyID}/delete":                 {Action: "security.passkey.delete", ResourceType: "passkey", PathValue: "passkeyID"},
	"POST /api/v1/servers":                                             {Action: "server.create", ResourceType: "server"},
	"POST /api/v1/repositories":                                        {Action: "repository.create", ResourceType: "repository"},
	"POST /api/v1/projects":                                            {Action: "project.create", ResourceType: "project"},
	"PUT /api/v1/projects/{projectID}":                                 {Action: "project.update", ResourceType: "project", PathValue: "projectID"},
	"PATCH /api/v1/projects/{projectID}":                               {Action: "project.update", ResourceType: "project", PathValue: "projectID"},
	"POST /api/v1/projects/{projectID}/run":                            {Action: "backup.run", ResourceType: "project", PathValue: "projectID"},
	"POST /api/v1/projects/{projectID}/retention-preview":              {Action: "retention.preview", ResourceType: "project", PathValue: "projectID"},
	"POST /api/v1/projects/{projectID}/snapshots/refresh":              {Action: "snapshot.refresh", ResourceType: "project", PathValue: "projectID"},
	"POST /api/v1/projects/{projectID}/snapshots/{snapshotID}/protect": {Action: "snapshot.protect", ResourceType: "snapshot", PathValue: "snapshotID"},
	"POST /api/v1/projects/{projectID}/snapshots/{snapshotID}/browse":  {Action: "snapshot.browse", ResourceType: "snapshot", PathValue: "snapshotID"},
	"POST /api/v1/projects/{projectID}/snapshots/{snapshotID}/restore": {Action: "snapshot.restore", ResourceType: "snapshot", PathValue: "snapshotID"},
	"POST /api/v1/notification-channels":                               {Action: "notification.channel.create", ResourceType: "notification_channel"},
	"PUT /api/v1/notification-channels/{channelID}":                    {Action: "notification.channel.update", ResourceType: "notification_channel", PathValue: "channelID"},
	"PATCH /api/v1/notification-channels/{channelID}":                  {Action: "notification.channel.update", ResourceType: "notification_channel", PathValue: "channelID"},
	"DELETE /api/v1/notification-channels/{channelID}":                 {Action: "notification.channel.archive", ResourceType: "notification_channel", PathValue: "channelID"},
	"POST /api/v1/notification-channels/{channelID}/test":              {Action: "notification.channel.test", ResourceType: "notification_channel", PathValue: "channelID"},
	"POST /api/v1/alerts/evaluate":                                     {Action: "notification.alert.evaluate", ResourceType: "alert"},
}

func (s *HTTPServer) auditPublic(spec auditSpec, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor := spec.Actor
		if actor == "" {
			if session, ok := s.adminAuth.session(r, time.Now()); ok {
				actor = session.Username
			} else {
				actor = "anonymous"
			}
		}
		metrics := &responseMetricsWriter{ResponseWriter: w}
		next.ServeHTTP(metrics, r)
		status := responseStatus(metrics)
		if status < http.StatusBadRequest && actor == "anonymous" && spec.SkipAnonymousSuccess {
			return
		}
		if status >= http.StatusBadRequest && !s.auditFailures.allow(spec.Action, authClientKey(r), status, s.service.now()) {
			return
		}
		if metrics.auditResourceID != "" {
			spec.ResourceID = metrics.auditResourceID
		}
		s.appendAuditEvent(r, spec, actor, status)
	})
}

func (s *HTTPServer) appendAuditEvent(r *http.Request, spec auditSpec, actor string, status int) {
	id, err := randomValue("aud", 12)
	if err != nil {
		s.logger.Error("generate audit event ID", "error", err)
		return
	}
	outcome := domain.AuditSucceeded
	if status >= http.StatusBadRequest {
		outcome = domain.AuditFailed
	}
	resourceID := spec.ResourceID
	if resourceID == "" && spec.PathValue != "" {
		resourceID = r.PathValue(spec.PathValue)
	}
	event := domain.AuditEvent{
		ID:           id,
		Actor:        actor,
		Action:       spec.Action,
		ResourceType: spec.ResourceType,
		ResourceID:   resourceID,
		Outcome:      outcome,
		ClientIP:     authClientKey(r),
		StatusCode:   status,
		CreatedAt:    s.service.now(),
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Second)
	defer cancel()
	if err := s.service.Store().AppendAuditEvent(ctx, event); err != nil {
		s.logger.Error("append audit event",
			"error", err,
			"action", event.Action,
			"outcome", event.Outcome,
			slog.Group("resource", "type", event.ResourceType, "id", event.ResourceID),
		)
	}
}

func responseStatus(metrics *responseMetricsWriter) int {
	if metrics.status == 0 {
		return http.StatusOK
	}
	return metrics.status
}
