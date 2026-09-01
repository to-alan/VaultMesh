package control

import (
	"net/http"
	"strconv"

	"github.com/to-alan/vaultmesh/internal/domain"
)

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
