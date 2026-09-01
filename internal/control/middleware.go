package control

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/to-alan/vaultmesh/internal/domain"
)

type agentContextKey struct{}

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
			w.Header().Set("Access-Control-Expose-Headers", "Retry-After, X-VaultMesh-API-Version")
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
			w.Header().Set("X-VaultMesh-API-Version", "1")
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
