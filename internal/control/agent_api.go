package control

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/to-alan/vaultmesh/internal/domain"
)

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
	// Merge configuration delivery into the heartbeat so each agent needs one
	// control-plane round trip per interval. A lagging revision triggers the
	// full config; otherwise the response stays empty.
	if heartbeat.AppliedRevision < server.DesiredRevision {
		config, err := s.service.DesiredConfig(r.Context(), server.ID)
		if err != nil {
			s.handleServiceError(w, err)
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]any{"config": config})
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
		var clockSkew *reportClockSkewError
		if errors.As(err, &clockSkew) {
			retryAfter := (clockSkew.retryAfter + time.Second - 1) / time.Second
			if retryAfter < 1 {
				retryAfter = 1
			}
			w.Header().Set("Retry-After", strconv.FormatInt(int64(retryAfter), 10))
			s.writeError(w, http.StatusBadRequest, "agent_clock_ahead", err.Error(), nil)
			return
		}
		s.writeError(w, http.StatusBadRequest, "validation_failed", err.Error(), nil)
		return
	}
	report.ServerID = server.ID
	var (
		snapshotInventory    []domain.Snapshot
		hasSnapshotInventory bool
		snapshotSyncedAt     time.Time
	)
	// Legacy Agent releases deliver the snapshot inventory inline in the run
	// report. The inventory is extracted, validated, and then stripped so the
	// bulky index is never persisted inside run history. An oversized inventory
	// must not poison an otherwise successful run: the report is accepted and
	// only the inventory is dropped.
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
			if len(snapshotInventory) > maxSnapshotInventoryEntries {
				snapshotInventory = nil
			} else {
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
			delete(report.Stats, "snapshots")
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

// agentSnapshots receives the Restic snapshot index through a dedicated,
// idempotent channel so an unbounded inventory can never reject or bloat an
// otherwise immutable run report.

func (s *HTTPServer) agentSnapshots(w http.ResponseWriter, r *http.Request) {
	server := agentFromContext(r.Context())
	var payload struct {
		ProjectID string            `json:"project_id"`
		Snapshots []domain.Snapshot `json:"snapshots"`
	}
	if !s.decodeJSONLimit(w, r, &payload, maxSnapshotInventoryBody) {
		return
	}
	receivedAt := s.service.now()
	projectID := strings.TrimSpace(payload.ProjectID)
	if projectID == "" {
		s.writeError(w, http.StatusBadRequest, "validation_failed", "project_id is required", nil)
		return
	}
	if len(payload.Snapshots) > maxSnapshotInventoryEntries {
		s.writeError(w, http.StatusRequestEntityTooLarge, "snapshot_inventory_too_large", "snapshot inventory contains too many entries", nil)
		return
	}
	project, err := s.service.Store().GetProject(r.Context(), projectID)
	if err != nil {
		s.handleServiceError(w, err)
		return
	}
	if project.ServerID != server.ID {
		s.writeError(w, http.StatusNotFound, "not_found", "project does not belong to this agent", nil)
		return
	}
	for index := range payload.Snapshots {
		snapshot := &payload.Snapshots[index]
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
	syncedAt := snapshotSyncTime(&receivedAt, receivedAt)
	if err := s.service.Store().ReplaceProjectSnapshots(r.Context(), projectID, server.ID, payload.Snapshots, syncedAt); err != nil {
		s.handleServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
