// Package control implements the VaultMesh control plane: business
// services, the management/agent HTTP API, administrator security, and
// notification delivery.
package control

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/to-alan/vaultmesh/internal/version"
)

type HTTPServer struct {
	service        *Service
	logger         *slog.Logger
	adminAuth      *adminAuthenticator
	passwordLimits *authAttemptLimiter
	secondFactors  *authAttemptLimiter
	auditFailures  *auditFailureSampler
	allowedOrigins map[string]struct{}
	// httpsReady gates backup and sync actions until TLS is configured.
	httpsReady bool
}

func NewHTTPServer(service *Service, logger *slog.Logger, adminConfig AdminAuthConfig, allowedOrigins []string, httpsReady bool) (*HTTPServer, error) {
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
		httpsReady:     httpsReady,
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
	mux.Handle("POST /api/v1/servers/{serverID}/detect", s.admin(http.HandlerFunc(s.createDetection)))
	mux.Handle("GET /api/v1/servers/{serverID}/detection", s.admin(http.HandlerFunc(s.getDetection)))
	mux.Handle("DELETE /api/v1/servers/{serverID}", s.admin(http.HandlerFunc(s.archiveServer)))
	mux.Handle("GET /api/v1/repositories", s.admin(http.HandlerFunc(s.listRepositories)))
	mux.Handle("POST /api/v1/repositories", s.admin(http.HandlerFunc(s.createRepository)))
	mux.Handle("DELETE /api/v1/repositories/{repositoryID}", s.admin(http.HandlerFunc(s.archiveRepository)))
	mux.Handle("GET /api/v1/projects", s.admin(http.HandlerFunc(s.listProjects)))
	mux.Handle("POST /api/v1/projects", s.admin(http.HandlerFunc(s.createProject)))
	mux.Handle("PUT /api/v1/projects/{projectID}", s.admin(http.HandlerFunc(s.replaceProject)))
	mux.Handle("PATCH /api/v1/projects/{projectID}", s.admin(http.HandlerFunc(s.updateProject)))
	mux.Handle("DELETE /api/v1/projects/{projectID}", s.admin(http.HandlerFunc(s.archiveProject)))
	mux.Handle("GET /api/v1/project-health", s.admin(http.HandlerFunc(s.listProjectHealth)))
	mux.Handle("POST /api/v1/projects/{projectID}/run", s.admin(s.backupGate(http.HandlerFunc(s.createManualRun))))
	mux.Handle("POST /api/v1/projects/{projectID}/retention-preview", s.admin(s.backupGate(http.HandlerFunc(s.createRetentionPreview))))
	mux.Handle("POST /api/v1/projects/{projectID}/snapshots/refresh", s.admin(s.backupGate(http.HandlerFunc(s.refreshSnapshots))))
	mux.Handle("POST /api/v1/projects/{projectID}/snapshots/{snapshotID}/protect", s.admin(s.backupGate(http.HandlerFunc(s.protectSnapshot))))
	mux.Handle("POST /api/v1/projects/{projectID}/snapshots/{snapshotID}/browse", s.admin(s.backupGate(http.HandlerFunc(s.browseSnapshot))))
	mux.Handle("POST /api/v1/projects/{projectID}/snapshots/{snapshotID}/restore", s.admin(s.backupGate(http.HandlerFunc(s.restoreSnapshot))))
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
	mux.Handle("PUT /api/v1/agent/detection", s.agent(http.HandlerFunc(s.agentDetection)))
	mux.Handle("PUT /api/v1/agent/snapshots", s.agent(http.HandlerFunc(s.agentSnapshots)))

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
	s.writeJSON(w, http.StatusOK, map[string]any{
		"name":        "VaultMesh",
		"version":     version.Version,
		"commit":      version.Commit,
		"https_ready": s.httpsReady,
	})
}

// backupGate blocks backup and sync actions until TLS is configured. Read
// access stays available so the console can be inspected over plain HTTP,
// but no command that would queue agent work or read repository contents is
// accepted until the deployment declares HTTPS via VAULTMESH_PUBLIC_API_URL
// (or an explicit VAULTMESH_HTTPS_ENABLED=true).
func (s *HTTPServer) backupGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.httpsReady {
			s.writeError(w, http.StatusForbidden, "https_required",
				"HTTPS is not configured; backup and sync actions are disabled. Set VAULTMESH_PUBLIC_API_URL to an https:// URL (or VAULTMESH_HTTPS_ENABLED=true) and restart.", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *HTTPServer) notFound(w http.ResponseWriter, _ *http.Request) {
	s.writeError(w, http.StatusNotFound, "not_found", "resource not found", nil)
}
