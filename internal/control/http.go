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

const maxRequestBody = 1 << 20

type HTTPServer struct {
	service        *Service
	logger         *slog.Logger
	adminAuth      *adminAuthenticator
	allowedOrigins map[string]struct{}
}

type agentContextKey struct{}

func NewHTTPServer(service *Service, logger *slog.Logger, adminConfig AdminAuthConfig, allowedOrigins []string) (*HTTPServer, error) {
	adminAuth, err := newAdminAuthenticator(adminConfig)
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
		allowedOrigins: origins,
	}, nil
}

func (s *HTTPServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /api/v1/meta", s.meta)
	mux.HandleFunc("POST /api/v1/enroll", s.enrollAgent)
	mux.HandleFunc("POST /api/v1/auth/login", s.login)
	mux.HandleFunc("POST /api/v1/auth/logout", s.logout)
	mux.Handle("GET /api/v1/auth/session", s.admin(http.HandlerFunc(s.session)))

	mux.Handle("GET /api/v1/dashboard", s.admin(http.HandlerFunc(s.dashboard)))
	mux.Handle("GET /api/v1/servers", s.admin(http.HandlerFunc(s.listServers)))
	mux.Handle("POST /api/v1/servers", s.admin(http.HandlerFunc(s.createServer)))
	mux.Handle("GET /api/v1/repositories", s.admin(http.HandlerFunc(s.listRepositories)))
	mux.Handle("POST /api/v1/repositories", s.admin(http.HandlerFunc(s.createRepository)))
	mux.Handle("GET /api/v1/projects", s.admin(http.HandlerFunc(s.listProjects)))
	mux.Handle("POST /api/v1/projects", s.admin(http.HandlerFunc(s.createProject)))
	mux.Handle("POST /api/v1/projects/{projectID}/run", s.admin(http.HandlerFunc(s.createManualRun)))
	mux.Handle("GET /api/v1/runs", s.admin(http.HandlerFunc(s.listRuns)))

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
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	if !s.adminAuth.authenticate(strings.TrimSpace(input.Username), input.Password) {
		s.writeError(w, http.StatusUnauthorized, "invalid_credentials", "username or password is incorrect", nil)
		return
	}
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

func (s *HTTPServer) createManualRun(w http.ResponseWriter, r *http.Request) {
	command, err := s.service.CreateManualRun(r.Context(), r.PathValue("projectID"))
	if err != nil {
		s.handleServiceError(w, err)
		return
	}
	s.writeJSON(w, http.StatusAccepted, command)
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

func (s *HTTPServer) dashboard(w http.ResponseWriter, r *http.Request) {
	dashboard, err := s.service.Store().Dashboard(r.Context(), time.Now().Add(-24*time.Hour))
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
	if report.ID == "" || report.IdempotencyKey == "" || report.ProjectID == "" || report.StartedAt.IsZero() {
		s.writeError(w, http.StatusBadRequest, "validation_failed", "run identity, project and start time are required", nil)
		return
	}
	if !validRunStatus(report.Status) {
		s.writeError(w, http.StatusBadRequest, "validation_failed", "invalid run status", nil)
		return
	}
	report.ServerID = server.ID
	if err := s.service.Store().UpsertRun(r.Context(), report); err != nil {
		s.handleServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *HTTPServer) admin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.adminAuth.session(r, time.Now()); !ok {
			s.writeError(w, http.StatusUnauthorized, "unauthorized", "administrator login required", nil)
			return
		}
		next.ServeHTTP(w, r)
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
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
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
		next.ServeHTTP(w, r)
		s.logger.Info("http request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(start))
	})
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
