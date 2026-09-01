package control

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/to-alan/vaultmesh/internal/store"
)

// maxRequestBody bounds the JSON body of every management-API request.
const maxRequestBody = 1 << 20

func (s *HTTPServer) decodeJSON(w http.ResponseWriter, r *http.Request, output any) bool {
	return s.decodeJSONLimit(w, r, output, maxRequestBody)
}

// decodeJSONLimit decodes a JSON body under an explicit size budget. Snapshot
// inventories legitimately outweigh the general 1 MiB management-API envelope,
// so the dedicated agent endpoint raises its own limit.

func (s *HTTPServer) decodeJSONLimit(w http.ResponseWriter, r *http.Request, output any, limit int64) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		s.writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "request content type must be application/json", nil)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
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
