package control

import (
	"errors"
	"strings"
	"time"

	"github.com/to-alan/vaultmesh/internal/domain"
)

const (
	maxAgentClockSkew  = 5 * time.Minute
	maxRunIDLength     = 128
	maxRunKeyLength    = 512
	maxRunErrorLength  = 16 << 10
	maxRunErrorCodeLen = 128

	// Snapshot inventories are reported through a dedicated endpoint and are
	// bounded independently of the general run report body.
	maxSnapshotInventoryEntries = 10000
	// 10000 entries require roughly 6 MiB of JSON; the extra headroom absorbs
	// tags and paths while staying far below any realistic DoS surface.
	maxSnapshotInventoryBody = 16 << 20
	// Legacy agents embed the inventory inline in the run report. The limit
	// must comfortably exceed the inventory cap, otherwise an oversized body
	// fails as invalid_json (a permanent rejection) and poisons the whole
	// run instead of triggering the graceful inventory drop below.
	maxRunReportBody = 4 << 20
)

type reportClockSkewError struct {
	message    string
	retryAfter time.Duration
}

func (e *reportClockSkewError) Error() string { return e.message }

// snapshotSyncTime preserves ordering for normally delayed run reports without
// allowing a badly skewed Agent clock to suppress future snapshot inventories.
func snapshotSyncTime(finishedAt *time.Time, receivedAt time.Time) time.Time {
	receivedAt = receivedAt.UTC()
	if finishedAt == nil {
		return receivedAt
	}
	candidate := finishedAt.UTC()
	if candidate.Before(receivedAt.Add(-maxAgentClockSkew)) || candidate.After(receivedAt.Add(maxAgentClockSkew)) {
		return receivedAt
	}
	return candidate
}

func validateRunReport(report domain.RunReport, receivedAt time.Time) error {
	if strings.TrimSpace(report.ID) == "" || strings.TrimSpace(report.IdempotencyKey) == "" || strings.TrimSpace(report.ProjectID) == "" {
		return errors.New("run identity and project are required")
	}
	if len(report.ID) > maxRunIDLength || len(report.ProjectID) > maxRunIDLength || len(report.IdempotencyKey) > maxRunKeyLength {
		return errors.New("run identity or idempotency key is too long")
	}
	if report.ScheduledAt.IsZero() || report.StartedAt.IsZero() {
		return errors.New("run schedule and start time are required")
	}
	if !validRunStatus(report.Status) {
		return errors.New("invalid run status")
	}
	if len(report.ErrorCode) > maxRunErrorCodeLen || len(report.ErrorMessage) > maxRunErrorLength {
		return errors.New("run error details are too long")
	}
	if report.ScheduledAt.After(report.StartedAt) {
		return errors.New("run schedule time must not be after its start time")
	}
	if report.StartedAt.After(receivedAt.Add(maxAgentClockSkew)) {
		return &reportClockSkewError{
			message:    "run start time is too far in the future",
			retryAfter: report.StartedAt.Sub(receivedAt.Add(maxAgentClockSkew)),
		}
	}
	if report.Status == domain.RunRunning {
		if report.FinishedAt != nil {
			return errors.New("a running run must not have a finish time")
		}
		return nil
	}
	if report.FinishedAt == nil {
		return errors.New("a terminal run must have a finish time")
	}
	if report.FinishedAt.Before(report.StartedAt) {
		return errors.New("run finish time must not be before its start time")
	}
	if report.FinishedAt.After(receivedAt.Add(maxAgentClockSkew)) {
		return &reportClockSkewError{
			message:    "run finish time is too far in the future",
			retryAfter: report.FinishedAt.Sub(receivedAt.Add(maxAgentClockSkew)),
		}
	}
	return nil
}

func validRunStatus(status string) bool {
	switch status {
	case domain.RunRunning, domain.RunSucceeded, domain.RunPartial, domain.RunFailed,
		domain.RunCanceled, domain.RunTimedOut, domain.RunUnknown, domain.RunSkipped:
		return true
	default:
		return false
	}
}
