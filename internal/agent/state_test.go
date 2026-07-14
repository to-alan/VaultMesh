package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/to-alan/vaultmesh/internal/domain"
)

func TestStatePersistsIdentityConfigAndDeduplicatesRuns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	state, err := OpenState(path)
	if err != nil {
		t.Fatal(err)
	}
	identity := domain.AgentIdentity{AgentID: "srv_test", Token: "secret-device-token"}
	if err := state.SetIdentity(identity); err != nil {
		t.Fatal(err)
	}
	config := domain.AgentConfig{Revision: 3}
	if err := state.SetConfig(config); err != nil {
		t.Fatal(err)
	}
	report := domain.RunReport{
		ID:             "run_1",
		IdempotencyKey: "project:time",
		ProjectID:      "project",
		ScheduledAt:    time.Now().UTC(),
		StartedAt:      time.Now().UTC(),
		Status:         domain.RunRunning,
	}
	claimed, err := state.BeginRun(report)
	if err != nil || !claimed {
		t.Fatalf("first claim: claimed=%v err=%v", claimed, err)
	}
	claimed, err = state.BeginRun(report)
	if err != nil || claimed {
		t.Fatalf("duplicate claim: claimed=%v err=%v", claimed, err)
	}

	reopened, err := OpenState(path)
	if err != nil {
		t.Fatal(err)
	}
	gotIdentity, ok := reopened.Identity()
	if !ok || gotIdentity != identity {
		t.Fatalf("unexpected identity: %#v, enrolled=%v", gotIdentity, ok)
	}
	if reopened.Config().Revision != 3 {
		t.Fatalf("unexpected revision %d", reopened.Config().Revision)
	}
	pending := reopened.PendingReports()
	if len(pending) != 1 || pending[0].Status != domain.RunUnknown {
		t.Fatalf("interrupted run was not recovered: %#v", pending)
	}
}

func TestStateMutationsRollBackWhenPersistenceFails(t *testing.T) {
	directory := t.TempDir()
	state, err := OpenState(filepath.Join(directory, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(directory); err != nil {
		t.Fatal(err)
	}

	identity := domain.AgentIdentity{AgentID: "srv_test", Token: "device-token"}
	if err := state.SetIdentity(identity); err == nil {
		t.Fatal("identity persistence unexpectedly succeeded")
	}
	if _, enrolled := state.Identity(); enrolled {
		t.Fatal("failed identity persistence changed in-memory state")
	}

	if err := state.SetConfig(domain.AgentConfig{Revision: 4}); err == nil {
		t.Fatal("configuration persistence unexpectedly succeeded")
	}
	if got := state.Config().Revision; got != 0 {
		t.Fatalf("failed configuration persistence changed revision to %d", got)
	}
}
