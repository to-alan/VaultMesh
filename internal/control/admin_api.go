package control

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/to-alan/vaultmesh/internal/domain"
)

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

func (s *HTTPServer) createDetection(w http.ResponseWriter, r *http.Request) {
	command, err := s.service.CreateDetectionCommand(r.Context(), r.PathValue("serverID"))
	if err != nil {
		s.handleServiceError(w, err)
		return
	}
	setAuditResourceID(w, command.ServerID)
	s.writeJSON(w, http.StatusAccepted, command)
}

func (s *HTTPServer) getDetection(w http.ResponseWriter, r *http.Request) {
	report, found, err := s.service.GetDetectionReport(r.Context(), r.PathValue("serverID"))
	if err != nil {
		s.handleServiceError(w, err)
		return
	}
	if !found {
		s.writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"available": true, "report": report})
}

func (s *HTTPServer) archiveServer(w http.ResponseWriter, r *http.Request) {
	server, err := s.service.ArchiveServer(r.Context(), r.PathValue("serverID"))
	if err != nil {
		s.handleServiceError(w, err)
		return
	}
	setAuditResourceID(w, server.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *HTTPServer) archiveRepository(w http.ResponseWriter, r *http.Request) {
	repository, err := s.service.ArchiveRepository(r.Context(), r.PathValue("repositoryID"))
	if err != nil {
		s.handleServiceError(w, err)
		return
	}
	setAuditResourceID(w, repository.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *HTTPServer) archiveProject(w http.ResponseWriter, r *http.Request) {
	project, err := s.service.ArchiveProject(r.Context(), r.PathValue("projectID"))
	if err != nil {
		s.handleServiceError(w, err)
		return
	}
	setAuditResourceID(w, project.ID)
	w.WriteHeader(http.StatusNoContent)
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

func (s *HTTPServer) dashboard(w http.ResponseWriter, r *http.Request) {
	dashboard, err := s.service.Dashboard(r.Context(), time.Now().Add(-24*time.Hour))
	if err != nil {
		s.handleServiceError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, dashboard)
}
