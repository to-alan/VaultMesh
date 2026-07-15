package control

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/to-alan/vaultmesh/internal/domain"
	"github.com/to-alan/vaultmesh/internal/store"
	"github.com/to-alan/vaultmesh/internal/version"
)

const (
	maxRequestBody     = 1 << 20
	maxAgentClockSkew  = 5 * time.Minute
	maxRunIDLength     = 128
	maxRunKeyLength    = 512
	maxRunErrorLength  = 16 << 10
	maxRunErrorCodeLen = 128
)

type HTTPServer struct {
	service        *Service
	logger         *slog.Logger
	adminAuth      *adminAuthenticator
	passwordLimits *authAttemptLimiter
	secondFactors  *authAttemptLimiter
	auditFailures  *auditFailureSampler
	allowedOrigins map[string]struct{}
}

type agentContextKey struct{}

func NewHTTPServer(service *Service, logger *slog.Logger, adminConfig AdminAuthConfig, allowedOrigins []string) (*HTTPServer, error) {
	adminAuth, err := newAdminAuthenticator(context.Background(), service, adminConfig)
	if err != nil {
		return nil, err
	}
	origins := make(map[string]struct{}, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		origins[origin] = struct{}{}
	}
	return &HTTPServer{
		service:        service,
		logger:         logger,
		adminAuth:      adminAuth,
		passwordLimits: newAuthAttemptLimiter(),
		secondFactors:  newAuthAttemptLimiter(),
		auditFailures:  newAuditFailureSampler(),
		allowedOrigins: origins,
	}, nil
}

func (s *HTTPServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /api/v1/meta", s.meta)
	mux.Handle("POST /api/v1/enroll", s.auditPublic(auditSpec{Action: "agent.enroll", Actor: "agent", ResourceType: "server"}, http.HandlerFunc(s.enrollAgent)))
	mux.Handle("POST /api/v1/auth/login", s.auditPublic(auditSpec{Action: "auth.password", Actor: "administrator", ResourceType: "account"}, http.HandlerFunc(s.login)))
	mux.Handle("POST /api/v1/auth/totp", s.auditPublic(auditSpec{Action: "auth.second_factor", Actor: "administrator", ResourceType: "account"}, http.HandlerFunc(s.completeTOTPLogin)))
	mux.HandleFunc("POST /api/v1/auth/passkey/begin", s.beginPasskeyLogin)
	mux.Handle("POST /api/v1/auth/passkey/finish", s.auditPublic(auditSpec{Action: "auth.passkey", Actor: "administrator", ResourceType: "account"}, http.HandlerFunc(s.finishPasskeyLogin)))
	mux.Handle("POST /api/v1/auth/logout", s.auditPublic(auditSpec{Action: "auth.logout", ResourceType: "account", SkipAnonymousSuccess: true}, http.HandlerFunc(s.logout)))
	mux.Handle("GET /api/v1/auth/session", s.admin(http.HandlerFunc(s.session)))
	mux.Handle("GET /api/v1/profile", s.admin(http.HandlerFunc(s.profile)))
	mux.Handle("POST /api/v1/profile/reauthenticate", s.admin(http.HandlerFunc(s.reauthenticate)))
	mux.Handle("POST /api/v1/profile/password", s.admin(http.HandlerFunc(s.changePassword)))
	mux.Handle("POST /api/v1/profile/totp/begin", s.admin(http.HandlerFunc(s.beginTOTP)))
	mux.Handle("POST /api/v1/profile/totp/enable", s.admin(http.HandlerFunc(s.enableTOTP)))
	mux.Handle("POST /api/v1/profile/totp/disable", s.admin(http.HandlerFunc(s.disableTOTP)))
	mux.Handle("POST /api/v1/profile/recovery-codes", s.admin(http.HandlerFunc(s.regenerateRecoveryCodes)))
	mux.Handle("POST /api/v1/profile/passkeys/register/begin", s.admin(http.HandlerFunc(s.beginPasskeyRegistration)))
	mux.Handle("POST /api/v1/profile/passkeys/register/finish", s.admin(http.HandlerFunc(s.finishPasskeyRegistration)))
	mux.Handle("POST /api/v1/profile/passkeys/{passkeyID}/delete", s.admin(http.HandlerFunc(s.deletePasskey)))

	mux.Handle("GET /api/v1/dashboard", s.admin(http.HandlerFunc(s.dashboard)))
	mux.Handle("GET /api/v1/servers", s.admin(http.HandlerFunc(s.listServers)))
	mux.Handle("POST /api/v1/servers", s.admin(http.HandlerFunc(s.createServer)))
	mux.Handle("GET /api/v1/repositories", s.admin(http.HandlerFunc(s.listRepositories)))
	mux.Handle("POST /api/v1/repositories", s.admin(http.HandlerFunc(s.createRepository)))
	mux.Handle("GET /api/v1/projects", s.admin(http.HandlerFunc(s.listProjects)))
	mux.Handle("POST /api/v1/projects", s.admin(http.HandlerFunc(s.createProject)))
	mux.Handle("PUT /api/v1/projects/{projectID}", s.admin(http.HandlerFunc(s.replaceProject)))
	mux.Handle("PATCH /api/v1/projects/{projectID}", s.admin(http.HandlerFunc(s.updateProject)))
	mux.Handle("GET /api/v1/project-health", s.admin(http.HandlerFunc(s.listProjectHealth)))
	mux.Handle("POST /api/v1/projects/{projectID}/run", s.admin(http.HandlerFunc(s.createManualRun)))
	mux.Handle("POST /api/v1/projects/{projectID}/retention-preview", s.admin(http.HandlerFunc(s.createRetentionPreview)))
	mux.Handle("POST /api/v1/projects/{projectID}/snapshots/refresh", s.admin(http.HandlerFunc(s.refreshSnapshots)))
	mux.Handle("POST /api/v1/projects/{projectID}/snapshots/{snapshotID}/protect", s.admin(http.HandlerFunc(s.protectSnapshot)))
	mux.Handle("POST /api/v1/projects/{projectID}/snapshots/{snapshotID}/browse", s.admin(http.HandlerFunc(s.browseSnapshot)))
	mux.Handle("POST /api/v1/projects/{projectID}/snapshots/{snapshotID}/restore", s.admin(http.HandlerFunc(s.restoreSnapshot)))
	mux.Handle("GET /api/v1/snapshots", s.admin(http.HandlerFunc(s.listSnapshots)))
	mux.Handle("GET /api/v1/runs", s.admin(http.HandlerFunc(s.listRuns)))
	mux.Handle("GET /api/v1/audit-events", s.admin(http.HandlerFunc(s.listAuditEvents)))
	mux.Handle("GET /api/v1/notification-channels", s.admin(http.HandlerFunc(s.listNotificationChannels)))
	mux.Handle("POST /api/v1/notification-channels", s.admin(http.HandlerFunc(s.createNotificationChannel)))
	mux.Handle("PUT /api/v1/notification-channels/{channelID}", s.admin(http.HandlerFunc(s.replaceNotificationChannel)))
	mux.Handle("PATCH /api/v1/notification-channels/{channelID}", s.admin(http.HandlerFunc(s.updateNotificationChannel)))
	mux.Handle("DELETE /api/v1/notification-channels/{channelID}", s.admin(http.HandlerFunc(s.deleteNotificationChannel)))
	mux.Handle("POST /api/v1/notification-channels/{channelID}/test", s.admin(http.HandlerFunc(s.testNotificationChannel)))
	mux.Handle("GET /api/v1/alert-incidents", s.admin(http.HandlerFunc(s.listAlertIncidents)))
	mux.Handle("GET /api/v1/notification-deliveries", s.admin(http.HandlerFunc(s.listNotificationDeliveries)))
	mux.Handle("POST /api/v1/alerts/evaluate", s.admin(http.HandlerFunc(s.evaluateAlerts)))

	mux.Handle("POST /api/v1/agent/heartbeat", s.agent(http.HandlerFunc(s.agentHeartbeat)))
	mux.Handle("GET /api/v1/agent/config", s.agent(http.HandlerFunc(s.agentConfig)))
	mux.Handle("GET /api/v1/agent/commands", s.agent(http.HandlerFunc(s.agentCommands)))
	mux.Handle("POST /api/v1/agent/runs", s.agent(http.HandlerFunc(s.agentRun)))

	mux.HandleFunc("/", s.notFound)
	return s.securityHeaders(s.cors(s.logging(mux)))
}

func (s *HTTPServer) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.service.Store().Ping(ctx); err != nil {
		s.writeError(w, http.StatusServiceUnavailable, "store_unavailable", "metadata store is unavailable", nil)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *HTTPServer) meta(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]string{
		"name":    "VaultMesh",
		"version": version.Version,
		"commit":  version.Commit,
	})
}

func (s *HTTPServer) login(w http.ResponseWriter, r *http.Request) {
	clientKey := authClientKey(r)
	if !s.allowAuthenticationAttempt(w, s.passwordLimits, clientKey, time.Now()) {
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	if !s.adminAuth.authenticate(strings.TrimSpace(input.Username), input.Password) {
		if s.recordAuthenticationFailure(w, s.passwordLimits, clientKey, time.Now()) {
			return
		}
		s.writeError(w, http.StatusUnauthorized, "invalid_credentials", "username or password is incorrect", nil)
		return
	}
	s.passwordLimits.recordSuccess(clientKey)
	if s.adminAuth.totpEnabled() {
		token, err := s.adminAuth.createPendingMFA(time.Now())
		if err != nil {
			s.logger.Error("create pending MFA session", "error", err)
			s.writeError(w, http.StatusInternalServerError, "internal_error", "two-step login could not be started", nil)
			return
		}
		s.adminAuth.setMFACookie(w, token)
		s.writeJSON(w, http.StatusAccepted, map[string]any{"mfa_required": true})
		return
	}
	s.issueAdminSession(w)
}

func (s *HTTPServer) completeTOTPLogin(w http.ResponseWriter, r *http.Request) {
	clientKey := authClientKey(r)
	if !s.allowAuthenticationAttempt(w, s.secondFactors, clientKey, time.Now()) {
		return
	}
	var input struct {
		Code string `json:"code"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	if err := s.adminAuth.completePendingMFA(r.Context(), r, input.Code, time.Now()); err != nil {
		if s.recordAuthenticationFailure(w, s.secondFactors, clientKey, time.Now()) {
			return
		}
		s.writeError(w, http.StatusUnauthorized, "invalid_second_factor", "verification code is invalid or the login attempt expired", nil)
		return
	}
	s.secondFactors.recordSuccess(clientKey)
	s.adminAuth.clearCookie(w, s.adminAuth.mfaCookieName(), s.adminAuth.cookieSecure)
	s.issueAdminSession(w)
}

func (s *HTTPServer) issueAdminSession(w http.ResponseWriter) {
	token, session, err := s.adminAuth.createSession(time.Now())
	if err != nil {
		s.logger.Error("create administrator session", "error", err)
		s.writeError(w, http.StatusInternalServerError, "internal_error", "administrator session could not be created", nil)
		return
	}
	s.adminAuth.setSessionCookie(w, token)
	s.writeJSON(w, http.StatusOK, map[string]any{
		"username":   session.Username,
		"expires_at": session.ExpiresAt,
	})
}

func (s *HTTPServer) logout(w http.ResponseWriter, r *http.Request) {
	s.adminAuth.deleteSession(r)
	s.adminAuth.clearSessionCookies(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *HTTPServer) session(w http.ResponseWriter, r *http.Request) {
	session, _ := s.adminAuth.session(r, time.Now())
	s.writeJSON(w, http.StatusOK, map[string]any{
		"username":   session.Username,
		"expires_at": session.ExpiresAt,
	})
}

func (s *HTTPServer) createServer(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name string `json:"name"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	result, err := s.service.CreateServer(r.Context(), input.Name)
	if err != nil {
		s.handleServiceError(w, err)
		return
	}
	setAuditResourceID(w, result.Server.ID)
	s.writeJSON(w, http.StatusCreated, result)
}

func (s *HTTPServer) listServers(w http.ResponseWriter, r *http.Request) {
	items, err := s.service.Store().ListServers(r.Context())
	if err != nil {
		s.handleServiceError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *HTTPServer) enrollAgent(w http.ResponseWriter, r *http.Request) {
	var input struct {
		EnrollmentToken string `json:"enrollment_token"`
		domain.AgentInfo
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	identity, err := s.service.EnrollAgent(r.Context(), input.EnrollmentToken, input.AgentInfo)
	if err != nil {
		s.handleServiceError(w, err)
		return
	}
	setAuditResourceID(w, identity.AgentID)
	s.writeJSON(w, http.StatusCreated, identity)
}

func (s *HTTPServer) createRepository(w http.ResponseWriter, r *http.Request) {
	var input domain.Repository
	if !s.decodeJSON(w, r, &input) {
		return
	}
	repository, err := s.service.CreateRepository(r.Context(), input)
	if err != nil {
		s.handleServiceError(w, err)
		return
	}
	setAuditResourceID(w, repository.ID)
	s.writeJSON(w, http.StatusCreated, repository)
}

func (s *HTTPServer) listRepositories(w http.ResponseWriter, r *http.Request) {
	items, err := s.service.Store().ListRepositories(r.Context())
	if err != nil {
		s.handleServiceError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *HTTPServer) createProject(w http.ResponseWriter, r *http.Request) {
	var input domain.Project
	if !s.decodeJSON(w, r, &input) {
		return
	}
	project, err := s.service.CreateProject(r.Context(), input)
	if err != nil {
		s.handleServiceError(w, err)
		return
	}
	setAuditResourceID(w, project.ID)
	s.writeJSON(w, http.StatusCreated, project)
}

func (s *HTTPServer) listProjects(w http.ResponseWriter, r *http.Request) {
	items, err := s.service.Store().ListProjects(r.Context())
	if err != nil {
		s.handleServiceError(w, err)
		return
	}
	now := s.service.now()
	for projectIndex := range items {
		items[projectIndex] = publicProject(items[projectIndex], now)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *HTTPServer) replaceProject(w http.ResponseWriter, r *http.Request) {
	var input domain.Project
	if !s.decodeJSON(w, r, &input) {
		return
	}
	project, err := s.service.UpdateProject(r.Context(), r.PathValue("projectID"), input)
	if err != nil {
		s.handleServiceError(w, err)
		return
	}
	setAuditResourceID(w, project.ID)
	s.writeJSON(w, http.StatusOK, project)
}

func (s *HTTPServer) listProjectHealth(w http.ResponseWriter, r *http.Request) {
	items, err := s.service.ProjectHealth(r.Context())
	if err != nil {
		s.handleServiceError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *HTTPServer) updateProject(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	if input.Enabled == nil {
		s.handleServiceError(w, validationError("enabled", "is required"))
		return
	}
	project, err := s.service.SetProjectEnabled(r.Context(), r.PathValue("projectID"), *input.Enabled)
	if err != nil {
		s.handleServiceError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, project)
}

func (s *HTTPServer) createManualRun(w http.ResponseWriter, r *http.Request) {
	command, err := s.service.CreateManualRun(r.Context(), r.PathValue("projectID"))
	if err != nil {
		s.handleServiceError(w, err)
		return
	}
	s.writeJSON(w, http.StatusAccepted, command)
}

func (s *HTTPServer) createRetentionPreview(w http.ResponseWriter, r *http.Request) {
	command, err := s.service.CreateRetentionPreview(r.Context(), r.PathValue("projectID"))
	if err != nil {
		s.handleServiceError(w, err)
		return
	}
	s.writeJSON(w, http.StatusAccepted, command)
}

func (s *HTTPServer) refreshSnapshots(w http.ResponseWriter, r *http.Request) {
	command, err := s.service.RefreshSnapshots(r.Context(), r.PathValue("projectID"))
	if err != nil {
		s.handleServiceError(w, err)
		return
	}
	s.writeJSON(w, http.StatusAccepted, command)
}

func (s *HTTPServer) protectSnapshot(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Protected *bool `json:"protected"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	if input.Protected == nil {
		s.handleServiceError(w, validationError("protected", "is required"))
		return
	}
	command, err := s.service.SetSnapshotProtected(r.Context(), r.PathValue("projectID"), r.PathValue("snapshotID"), *input.Protected)
	if err != nil {
		s.handleServiceError(w, err)
		return
	}
	s.writeJSON(w, http.StatusAccepted, command)
}

func (s *HTTPServer) browseSnapshot(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Path string `json:"path"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	command, err := s.service.BrowseSnapshot(r.Context(), r.PathValue("projectID"), r.PathValue("snapshotID"), input.Path)
	if err != nil {
		s.handleServiceError(w, err)
		return
	}
	s.writeJSON(w, http.StatusAccepted, command)
}

func (s *HTTPServer) restoreSnapshot(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Path string `json:"path"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	command, err := s.service.RestoreSnapshot(r.Context(), r.PathValue("projectID"), r.PathValue("snapshotID"), input.Path)
	if err != nil {
		s.handleServiceError(w, err)
		return
	}
	s.writeJSON(w, http.StatusAccepted, command)
}

func (s *HTTPServer) listSnapshots(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.service.Store().ListSnapshots(r.Context(), strings.TrimSpace(r.URL.Query().Get("project_id")), limit)
	if err != nil {
		s.handleServiceError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *HTTPServer) listRuns(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.service.Store().ListRuns(r.Context(), limit)
	if err != nil {
		s.handleServiceError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *HTTPServer) listAuditEvents(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.service.Store().ListAuditEvents(r.Context(), limit)
	if err != nil {
		s.handleServiceError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *HTTPServer) listNotificationChannels(w http.ResponseWriter, r *http.Request) {
	items, err := s.service.ListNotificationChannels(r.Context())
	if err != nil {
		s.handleServiceError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *HTTPServer) createNotificationChannel(w http.ResponseWriter, r *http.Request) {
	var input domain.NotificationChannel
	if !s.decodeJSON(w, r, &input) {
		return
	}
	channel, err := s.service.CreateNotificationChannel(r.Context(), input)
	if err != nil {
		s.handleServiceError(w, err)
		return
	}
	setAuditResourceID(w, channel.ID)
	s.writeJSON(w, http.StatusCreated, channel)
}

func (s *HTTPServer) replaceNotificationChannel(w http.ResponseWriter, r *http.Request) {
	var input domain.NotificationChannel
	if !s.decodeJSON(w, r, &input) {
		return
	}
	channel, err := s.service.UpdateNotificationChannel(r.Context(), r.PathValue("channelID"), input)
	if err != nil {
		s.handleServiceError(w, err)
		return
	}
	setAuditResourceID(w, channel.ID)
	s.writeJSON(w, http.StatusOK, channel)
}

func (s *HTTPServer) updateNotificationChannel(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	if input.Enabled == nil {
		s.writeError(w, http.StatusBadRequest, "validation_failed", "enabled is required", nil)
		return
	}
	channel, err := s.service.SetNotificationChannelEnabled(r.Context(), r.PathValue("channelID"), *input.Enabled)
	if err != nil {
		s.handleServiceError(w, err)
		return
	}
	setAuditResourceID(w, channel.ID)
	s.writeJSON(w, http.StatusOK, channel)
}

func (s *HTTPServer) deleteNotificationChannel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("channelID")
	if err := s.service.Store().ArchiveNotificationChannel(r.Context(), id, s.service.now()); err != nil {
		s.handleServiceError(w, err)
		return
	}
	setAuditResourceID(w, id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *HTTPServer) testNotificationChannel(w http.ResponseWriter, r *http.Request) {
	if err := s.service.TestNotificationChannel(r.Context(), r.PathValue("channelID")); err != nil {
		s.handleServiceError(w, err)
		return
	}
	setAuditResourceID(w, r.PathValue("channelID"))
	w.WriteHeader(http.StatusNoContent)
}

func (s *HTTPServer) listAlertIncidents(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.service.Store().ListAlertIncidents(r.Context(), limit)
	if err != nil {
		s.handleServiceError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *HTTPServer) listNotificationDeliveries(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.service.Store().ListNotificationDeliveries(r.Context(), limit)
	if err != nil {
		s.handleServiceError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *HTTPServer) evaluateAlerts(w http.ResponseWriter, r *http.Request) {
	if err := s.service.EvaluateAlerts(r.Context()); err != nil {
		s.handleServiceError(w, err)
		return
	}
	if err := s.service.DeliverNotifications(r.Context()); err != nil {
		s.handleServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *HTTPServer) dashboard(w http.ResponseWriter, r *http.Request) {
	dashboard, err := s.service.Dashboard(r.Context(), time.Now().Add(-24*time.Hour))
	if err != nil {
		s.handleServiceError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, dashboard)
}

func (s *HTTPServer) agentHeartbeat(w http.ResponseWriter, r *http.Request) {
	server := agentFromContext(r.Context())
	var heartbeat domain.Heartbeat
	if !s.decodeJSON(w, r, &heartbeat) {
		return
	}
	if err := s.service.Heartbeat(r.Context(), server.ID, heartbeat); err != nil {
		s.handleServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *HTTPServer) agentConfig(w http.ResponseWriter, r *http.Request) {
	server := agentFromContext(r.Context())
	config, err := s.service.DesiredConfig(r.Context(), server.ID)
	if err != nil {
		s.handleServiceError(w, err)
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if after >= config.Revision {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	s.writeJSON(w, http.StatusOK, config)
}

func (s *HTTPServer) agentCommands(w http.ResponseWriter, r *http.Request) {
	server := agentFromContext(r.Context())
	commands, err := s.service.ClaimCommands(r.Context(), server.ID)
	if err != nil {
		s.handleServiceError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"items": commands})
}

func (s *HTTPServer) agentRun(w http.ResponseWriter, r *http.Request) {
	server := agentFromContext(r.Context())
	var report domain.RunReport
	if !s.decodeJSON(w, r, &report) {
		return
	}
	receivedAt := s.service.now()
	if err := validateRunReport(report, receivedAt); err != nil {
		s.writeError(w, http.StatusBadRequest, "validation_failed", err.Error(), nil)
		return
	}
	report.ServerID = server.ID
	var (
		snapshotInventory    []domain.Snapshot
		hasSnapshotInventory bool
		snapshotSyncedAt     time.Time
	)
	if report.Status == domain.RunSucceeded && report.Stats != nil {
		if raw, ok := report.Stats["snapshots"]; ok {
			encoded, err := json.Marshal(raw)
			if err != nil {
				s.writeError(w, http.StatusBadRequest, "validation_failed", "snapshot inventory is invalid", nil)
				return
			}
			if err := json.Unmarshal(encoded, &snapshotInventory); err != nil {
				s.writeError(w, http.StatusBadRequest, "validation_failed", "snapshot inventory is invalid", nil)
				return
			}
			if len(snapshotInventory) > 10000 {
				s.writeError(w, http.StatusRequestEntityTooLarge, "snapshot_inventory_too_large", "snapshot inventory contains too many entries", nil)
				return
			}
			for index := range snapshotInventory {
				snapshot := &snapshotInventory[index]
				if !fullResticSnapshotID.MatchString(snapshot.ID) || snapshot.Time.IsZero() {
					s.writeError(w, http.StatusBadRequest, "validation_failed", "snapshot inventory contains an invalid ID or timestamp", nil)
					return
				}
				snapshot.Protected = false
				for _, tag := range snapshot.Tags {
					if tag == protectedSnapshotTag {
						snapshot.Protected = true
						break
					}
				}
			}
			hasSnapshotInventory = true
			snapshotSyncedAt = snapshotSyncTime(report.FinishedAt, receivedAt)
		}
	}
	if err := s.service.Store().UpsertRun(r.Context(), report); err != nil {
		s.handleServiceError(w, err)
		return
	}
	if hasSnapshotInventory {
		if err := s.service.Store().ReplaceProjectSnapshots(r.Context(), report.ProjectID, server.ID, snapshotInventory, snapshotSyncedAt); err != nil {
			s.handleServiceError(w, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *HTTPServer) admin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session, ok := s.adminAuth.session(r, time.Now())
		if !ok {
			s.writeError(w, http.StatusUnauthorized, "unauthorized", "administrator login required", nil)
			return
		}
		spec, audited := adminAuditSpecs[r.Pattern]
		if !audited {
			next.ServeHTTP(w, r)
			return
		}
		metrics := &responseMetricsWriter{ResponseWriter: w}
		next.ServeHTTP(metrics, r)
		if metrics.auditResourceID != "" {
			spec.ResourceID = metrics.auditResourceID
		}
		s.appendAuditEvent(r, spec, session.Username, responseStatus(metrics))
	})
}

func (s *HTTPServer) agent(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r.Header.Get("Authorization"))
		server, err := s.service.AuthenticateAgent(r.Context(), token)
		if err != nil {
			s.writeError(w, http.StatusUnauthorized, "unauthorized", "valid agent credential required", nil)
			return
		}
		ctx := context.WithValue(r.Context(), agentContextKey{}, server)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *HTTPServer) notFound(w http.ResponseWriter, _ *http.Request) {
	s.writeError(w, http.StatusNotFound, "not_found", "resource not found", nil)
}

func (s *HTTPServer) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if origin != "" {
			if _, allowed := s.allowedOrigins[origin]; !allowed {
				s.writeError(w, http.StatusForbidden, "origin_forbidden", "request origin is not allowed", nil)
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept")
			w.Header().Set("Access-Control-Max-Age", "600")
			w.Header().Add("Vary", "Origin")
			w.Header().Add("Vary", "Access-Control-Request-Method")
			w.Header().Add("Vary", "Access-Control-Request-Headers")
		}
		if r.Method == http.MethodOptions {
			if origin == "" {
				s.writeError(w, http.StatusBadRequest, "origin_required", "CORS preflight requires an Origin header", nil)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// snapshotSyncTime preserves ordering for normally delayed run reports without
// allowing a badly skewed Agent clock to suppress future snapshot inventories.
func snapshotSyncTime(finishedAt *time.Time, receivedAt time.Time) time.Time {
	receivedAt = receivedAt.UTC()
	if finishedAt == nil {
		return receivedAt
	}
	candidate := finishedAt.UTC()
	if candidate.Before(receivedAt.Add(-maxAgentClockSkew)) || candidate.After(receivedAt.Add(maxAgentClockSkew)) {
		return receivedAt
	}
	return candidate
}

func validateRunReport(report domain.RunReport, receivedAt time.Time) error {
	if strings.TrimSpace(report.ID) == "" || strings.TrimSpace(report.IdempotencyKey) == "" || strings.TrimSpace(report.ProjectID) == "" {
		return errors.New("run identity and project are required")
	}
	if len(report.ID) > maxRunIDLength || len(report.ProjectID) > maxRunIDLength || len(report.IdempotencyKey) > maxRunKeyLength {
		return errors.New("run identity or idempotency key is too long")
	}
	if report.ScheduledAt.IsZero() || report.StartedAt.IsZero() {
		return errors.New("run schedule and start time are required")
	}
	if !validRunStatus(report.Status) {
		return errors.New("invalid run status")
	}
	if len(report.ErrorCode) > maxRunErrorCodeLen || len(report.ErrorMessage) > maxRunErrorLength {
		return errors.New("run error details are too long")
	}
	if report.ScheduledAt.After(report.StartedAt) {
		return errors.New("run schedule time must not be after its start time")
	}
	if report.StartedAt.After(receivedAt.Add(maxAgentClockSkew)) {
		return errors.New("run start time is too far in the future")
	}
	if report.Status == domain.RunRunning {
		if report.FinishedAt != nil {
			return errors.New("a running run must not have a finish time")
		}
		return nil
	}
	if report.FinishedAt == nil {
		return errors.New("a terminal run must have a finish time")
	}
	if report.FinishedAt.Before(report.StartedAt) {
		return errors.New("run finish time must not be before its start time")
	}
	if report.FinishedAt.After(receivedAt.Add(maxAgentClockSkew)) {
		return errors.New("run finish time is too far in the future")
	}
	return nil
}

func (s *HTTPServer) allowAuthenticationAttempt(w http.ResponseWriter, limiter *authAttemptLimiter, clientKey string, now time.Time) bool {
	if retryAfter, blocked := limiter.retryAfter(clientKey, now); blocked {
		s.writeAuthenticationRateLimit(w, retryAfter)
		return false
	}
	return true
}

func (s *HTTPServer) recordAuthenticationFailure(w http.ResponseWriter, limiter *authAttemptLimiter, clientKey string, now time.Time) bool {
	if retryAfter, blocked := limiter.recordFailure(clientKey, now); blocked {
		s.writeAuthenticationRateLimit(w, retryAfter)
		return true
	}
	return false
}

func (s *HTTPServer) writeAuthenticationRateLimit(w http.ResponseWriter, retryAfter time.Duration) {
	seconds := int((retryAfter + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	s.writeError(w, http.StatusTooManyRequests, "rate_limited", "too many authentication attempts; retry later", nil)
}

func (s *HTTPServer) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *HTTPServer) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		metrics := &responseMetricsWriter{ResponseWriter: w}
		next.ServeHTTP(metrics, r)
		status := responseStatus(metrics)
		s.logger.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", status,
			"response_bytes", metrics.bytes,
			"duration", time.Since(start),
		)
	})
}

type responseMetricsWriter struct {
	http.ResponseWriter
	status          int
	bytes           int
	auditResourceID string
}

func (w *responseMetricsWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseMetricsWriter) Write(payload []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	written, err := w.ResponseWriter.Write(payload)
	w.bytes += written
	return written, err
}

// Unwrap lets http.ResponseController reach optional capabilities implemented
// by the underlying writer without coupling this metrics wrapper to them.
func (w *responseMetricsWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func setAuditResourceID(w http.ResponseWriter, resourceID string) {
	for w != nil {
		if metrics, ok := w.(*responseMetricsWriter); ok {
			metrics.auditResourceID = resourceID
			return
		}
		unwrapper, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return
		}
		next := unwrapper.Unwrap()
		if next == w {
			return
		}
		w = next
	}
}

func (s *HTTPServer) decodeJSON(w http.ResponseWriter, r *http.Request, output any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_json", "request body must contain valid JSON", nil)
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		s.writeError(w, http.StatusBadRequest, "invalid_json", "request body must contain a single JSON value", nil)
		return false
	}
	return true
}

func (s *HTTPServer) handleServiceError(w http.ResponseWriter, err error) {
	var validation *ValidationError
	switch {
	case errors.As(err, &validation):
		s.writeError(w, http.StatusUnprocessableEntity, "validation_failed", validation.Message, map[string]string{"field": validation.Field})
	case errors.Is(err, store.ErrNotFound):
		s.writeError(w, http.StatusNotFound, "not_found", "referenced resource was not found", nil)
	case errors.Is(err, store.ErrConflict):
		s.writeError(w, http.StatusConflict, "conflict", "resource already exists or conflicts with current state", nil)
	case errors.Is(err, store.ErrInvalidEnrollment):
		s.writeError(w, http.StatusUnauthorized, "invalid_enrollment", "enrollment token is invalid, expired, or already used", nil)
	default:
		s.logger.Error("request failed", "error", err)
		s.writeError(w, http.StatusInternalServerError, "internal_error", "request could not be completed", nil)
	}
}

func (s *HTTPServer) writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		s.logger.Error("write JSON response", "error", err)
	}
}

func (s *HTTPServer) writeError(w http.ResponseWriter, status int, code, message string, details any) {
	s.writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
			"details": details,
		},
	})
}

func bearerToken(header string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(header, prefix))
}

func agentFromContext(ctx context.Context) domain.Server {
	server, _ := ctx.Value(agentContextKey{}).(domain.Server)
	return server
}

func validRunStatus(status string) bool {
	switch status {
	case domain.RunRunning, domain.RunSucceeded, domain.RunPartial, domain.RunFailed,
		domain.RunCanceled, domain.RunTimedOut, domain.RunUnknown:
		return true
	default:
		return false
	}
}
