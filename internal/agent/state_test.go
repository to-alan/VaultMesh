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

func TestStateConfigRollbackGuardAndOneShotAccept(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	state, err := OpenState(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.SetConfig(domain.AgentConfig{Revision: 7}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetConfig(domain.AgentConfig{Revision: 6}); err == nil {
		t.Fatal("rollback was accepted without the explicit override")
	}

	// The documented control-plane restore procedure resets the revision
	// counter; --accept-rollback allows exactly one lower-revision apply.
	state.AcceptRollback()
	if err := state.SetConfig(domain.AgentConfig{Revision: 4}); err != nil {
		t.Fatalf("rollback override did not apply the restored config: %v", err)
	}
	if err := state.SetConfig(domain.AgentConfig{Revision: 3}); err == nil {
		t.Fatal("rollback acceptance was not one-shot")
	}

	reopened, err := OpenState(path)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Config().Revision != 4 {
		t.Fatalf("restored revision did not persist: %d", reopened.Config().Revision)
	}
}

func TestSnapshotInventoryOutboxKeepsLatestPerProject(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	state, err := OpenState(path)
	if err != nil {
		t.Fatal(err)
	}
	first := domain.Snapshot{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Time: time.Now().UTC()}
	second := domain.Snapshot{ID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Time: time.Now().UTC()}
	if err := state.QueueSnapshotInventory("prj_one", []domain.Snapshot{first}); err != nil {
		t.Fatal(err)
	}
	if err := state.QueueSnapshotInventory("prj_two", []domain.Snapshot{second}); err != nil {
		t.Fatal(err)
	}
	// A newer inventory for the same project replaces the pending delivery.
	if err := state.QueueSnapshotInventory("prj_one", []domain.Snapshot{second, first}); err != nil {
		t.Fatal(err)
	}

	pending := state.PendingSnapshotInventories()
	if len(pending) != 2 {
		t.Fatalf("expected two pending inventories, got %#v", pending)
	}
	byProject := make(map[string]PendingSnapshotInventory, len(pending))
	for _, inventory := range pending {
		byProject[inventory.ProjectID] = inventory
	}
	if inventory, ok := byProject["prj_one"]; !ok || len(inventory.Snapshots) != 2 || inventory.Snapshots[0].ID != second.ID {
		t.Fatalf("newest inventory did not replace the pending delivery: %#v", inventory)
	}

	if err := state.AckSnapshotInventory("prj_one"); err != nil {
		t.Fatal(err)
	}
	pending = state.PendingSnapshotInventories()
	if len(pending) != 1 || pending[0].ProjectID != "prj_two" {
		t.Fatalf("unexpected pending inventories after ack: %#v", pending)
	}

	reopened, err := OpenState(path)
	if err != nil {
		t.Fatal(err)
	}
	pending = reopened.PendingSnapshotInventories()
	if len(pending) != 1 || pending[0].ProjectID != "prj_two" || len(pending[0].Snapshots) != 1 {
		t.Fatalf("inventory outbox did not survive a restart: %#v", pending)
	}
}

func TestAckReportRollsBackWhenPersistenceFails(t *testing.T) {
	directory := t.TempDir()
	state, err := OpenState(filepath.Join(directory, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	report := domain.RunReport{
		ID: "run_pending", IdempotencyKey: "project:pending", ProjectID: "project",
		ScheduledAt: now, StartedAt: now, Status: domain.RunSucceeded,
	}
	if claimed, err := state.BeginRun(report); err != nil || !claimed {
		t.Fatalf("begin run: claimed=%v err=%v", claimed, err)
	}
	if err := os.RemoveAll(directory); err != nil {
		t.Fatal(err)
	}
	if err := state.AckReport(report.ID); err == nil {
		t.Fatal("report acknowledgement unexpectedly persisted")
	}
	pending := state.PendingReports()
	if len(pending) != 1 || pending[0].ID != report.ID {
		t.Fatalf("failed acknowledgement removed the pending report: %#v", pending)
	}
}

func TestQuarantineReportPersistsWithoutBlockingOutbox(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	state, err := OpenState(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	report := domain.RunReport{
		ID: "run_rejected", IdempotencyKey: "project:rejected", ProjectID: "project",
		ScheduledAt: now, StartedAt: now, Status: domain.RunSucceeded,
	}
	if claimed, err := state.BeginRun(report); err != nil || !claimed {
		t.Fatalf("begin run: claimed=%v err=%v", claimed, err)
	}
	if err := state.QuarantineReport(report.ID, "control plane returned validation_failed", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if pending := state.PendingReports(); len(pending) != 0 {
		t.Fatalf("quarantined report remained in outbox: %#v", pending)
	}

	reopened, err := OpenState(path)
	if err != nil {
		t.Fatal(err)
	}
	rejected := reopened.RejectedReports()
	if len(rejected) != 1 || rejected[0].Report.ID != report.ID || rejected[0].Reason == "" {
		t.Fatalf("unexpected rejected reports: %#v", rejected)
	}
	if !rejected[0].RejectedAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("unexpected rejection time: %v", rejected[0].RejectedAt)
	}
}

func TestQuarantineReportRollsBackWhenPersistenceFails(t *testing.T) {
	directory := t.TempDir()
	state, err := OpenState(filepath.Join(directory, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	report := domain.RunReport{
		ID: "run_pending", IdempotencyKey: "project:pending", ProjectID: "project",
		ScheduledAt: now, StartedAt: now, Status: domain.RunSucceeded,
	}
	if claimed, err := state.BeginRun(report); err != nil || !claimed {
		t.Fatalf("begin run: claimed=%v err=%v", claimed, err)
	}
	if err := os.RemoveAll(directory); err != nil {
		t.Fatal(err)
	}
	if err := state.QuarantineReport(report.ID, "permanent rejection", now); err == nil {
		t.Fatal("report quarantine unexpectedly persisted")
	}
	if pending := state.PendingReports(); len(pending) != 1 || pending[0].ID != report.ID {
		t.Fatalf("failed quarantine removed the pending report: %#v", pending)
	}
	if rejected := state.RejectedReports(); len(rejected) != 0 {
		t.Fatalf("failed quarantine changed rejected reports: %#v", rejected)
	}
}

func TestFinishRunRequeuesAChangedQuarantinedReport(t *testing.T) {
	state, err := OpenState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	running := domain.RunReport{
		ID: "run_progress", IdempotencyKey: "project:progress", ProjectID: "project",
		ScheduledAt: now, StartedAt: now, Status: domain.RunRunning,
	}
	if claimed, err := state.BeginRun(running); err != nil || !claimed {
		t.Fatalf("begin run: claimed=%v err=%v", claimed, err)
	}
	if err := state.QuarantineReport(running.ID, "running form was rejected", now); err != nil {
		t.Fatal(err)
	}
	finishedAt := now.Add(time.Minute)
	terminal := running
	terminal.Status = domain.RunSucceeded
	terminal.FinishedAt = &finishedAt
	if err := state.FinishRun(terminal); err != nil {
		t.Fatal(err)
	}
	if rejected := state.RejectedReports(); len(rejected) != 0 {
		t.Fatalf("superseded rejection was retained: %#v", rejected)
	}
	pending := state.PendingReports()
	if len(pending) != 1 || pending[0].Status != domain.RunSucceeded {
		t.Fatalf("terminal form was not requeued: %#v", pending)
	}
}
