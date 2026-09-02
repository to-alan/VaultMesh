package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/to-alan/vaultmesh/internal/domain"
)

// Reproduce the user's path: agent claims commands -> detect -> ReportDetection.
func TestDetectCommandExecution(t *testing.T) {
	// Fake control plane
	commandsIssued := false
	var reportReceived *domain.DetectionReport
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/agent/commands":
			if !commandsIssued {
				commandsIssued = true
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"items": []domain.Command{{ID: "cmd_detect01", ServerID: "srv_x", Type: "detect"}},
				})
			} else {
				_ = json.NewEncoder(w).Encode(map[string]any{"items": []domain.Command{}})
			}
		case r.URL.Path == "/api/v1/agent/detection":
			var report domain.DetectionReport
			body, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(body, &report); err != nil {
				t.Errorf("bad report: %v", err)
				w.WriteHeader(400)
				return
			}
			reportReceived = &report
			w.WriteHeader(204)
		case r.URL.Path == "/api/v1/agent/heartbeat":
			w.WriteHeader(204)
		default:
			w.WriteHeader(204)
		}
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "test")
	if err != nil {
		t.Fatal(err)
	}

	// Fake binaries so Detect's tool checks have something to run.
	bin := t.TempDir()
	for _, tool := range []string{"restic", "docker", "mysqldump", "pg_dump"} {
		_ = os.WriteFile(filepath.Join(bin, tool), []byte("#!/bin/sh\necho fake 1.0\n"), 0o700)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))

	runner := NewRunnerWithTools(filepath.Join(bin, "restic"), filepath.Join(bin, "mysqldump"), filepath.Join(bin, "pg_dump"), filepath.Join(bin, "docker"), "")
	result := runner.Detect(context.Background(), "cmd_detect01")
	if result.Status != domain.RunSucceeded {
		t.Fatalf("detect failed: %+v", result)
	}
	if result.DetectionReport == nil || len(result.DetectionReport.Apps) == 0 && len(result.DetectionReport.Containers) == 0 && len(result.DetectionReport.Databases) == 0 {
		t.Logf("report empty (fine on a clean box): %+v", result.DetectionReport)
	}

	// Now the real fetchCommands path via a manager-less run:
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	identity := domain.AgentIdentity{AgentID: "srv_x", Token: "tok"}
	RunDetection(context.Background(), client, runner, identity, domain.Command{ID: "cmd_detect01", ServerID: "srv_x", Type: "detect"}, logger)
	deadline := time.Now().Add(2 * time.Second)
	for reportReceived == nil && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if reportReceived == nil {
		t.Fatal("detection report never delivered to control plane")
	}
	t.Logf("report delivered: containers=%d databases=%d apps=%d",
		len(reportReceived.Containers), len(reportReceived.Databases), len(reportReceived.Apps))
}
