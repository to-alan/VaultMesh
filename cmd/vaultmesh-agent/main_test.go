package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/to-alan/vaultmesh/internal/agent"
	"github.com/to-alan/vaultmesh/internal/domain"
)

func TestFlushReportsQuarantinesPermanentRejectionAndContinues(t *testing.T) {
	state := openReportTestState(t)
	now := time.Now().UTC()
	addPendingReport(t, state, "run_rejected", now)
	addPendingReport(t, state, "run_accepted", now.Add(time.Second))

	var accepted atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var report domain.RunReport
		if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
			t.Errorf("decode report: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if report.ID == "run_rejected" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"code":"validation_failed","message":"report is malformed"}}`)
			return
		}
		accepted.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client, err := agent.NewClient(server.URL, "test")
	if err != nil {
		t.Fatal(err)
	}
	flushReports(context.Background(), client, state, domain.AgentIdentity{Token: "device-token"}, discardLogger())
	if accepted.Load() != 1 {
		t.Fatalf("accepted reports = %d, want 1", accepted.Load())
	}
	if pending := state.PendingReports(); len(pending) != 0 {
		t.Fatalf("outbox remained blocked: %#v", pending)
	}
	rejected := state.RejectedReports()
	if len(rejected) != 1 || rejected[0].Report.ID != "run_rejected" {
		t.Fatalf("unexpected rejected reports: %#v", rejected)
	}
}

func TestFlushReportsStopsAtRetryableFailure(t *testing.T) {
	state := openReportTestState(t)
	now := time.Now().UTC()
	addPendingReport(t, state, "run_retry", now)
	addPendingReport(t, state, "run_later", now.Add(time.Second))

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"code":"internal_error","message":"temporarily unavailable"}}`)
	}))
	defer server.Close()
	client, err := agent.NewClient(server.URL, "test")
	if err != nil {
		t.Fatal(err)
	}
	flushReports(context.Background(), client, state, domain.AgentIdentity{Token: "device-token"}, discardLogger())
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want 1", attempts.Load())
	}
	if pending := state.PendingReports(); len(pending) != 2 {
		t.Fatalf("retryable failure changed outbox: %#v", pending)
	}
	if rejected := state.RejectedReports(); len(rejected) != 0 {
		t.Fatalf("retryable failure was quarantined: %#v", rejected)
	}
}

func openReportTestState(t *testing.T) *agent.StateStore {
	t.Helper()
	state, err := agent.OpenState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func addPendingReport(t *testing.T, state *agent.StateStore, id string, startedAt time.Time) {
	t.Helper()
	finishedAt := startedAt.Add(time.Second)
	report := domain.RunReport{
		ID: id, IdempotencyKey: "project:" + id, ProjectID: "project",
		ScheduledAt: startedAt, StartedAt: startedAt, FinishedAt: &finishedAt, Status: domain.RunSucceeded,
	}
	claimed, err := state.BeginRun(report)
	if err != nil || !claimed {
		t.Fatalf("begin report %s: claimed=%v err=%v", id, claimed, err)
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
