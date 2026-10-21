package artifactcache

import (
	"context"
	"time"
)

// ToolRecoveryCompletion is a durable publication receipt, not permission to
// release a source or proof of its ownership. The caller commits this result
// before separately releasing its exact pin and removing the stopped source.
type ToolRecoveryCompletion struct {
	Pin         ToolRecoveryPin `json:"pin"`
	Generation  string          `json:"generation"`
	CompletedAt time.Time       `json:"completed_at"`
}

type ToolRecoveryPublicationReport struct {
	SchemaVersion int             `json:"schema_version"`
	Root          string          `json:"root"`
	Pin           ToolRecoveryPin `json:"pin"`
	Generation    string          `json:"generation,omitempty"`
	CompletedAt   time.Time       `json:"completed_at"`
	Completed     bool            `json:"completed"`
	Partial       bool            `json:"partial"`
	Error         string          `json:"error,omitempty"`
	PendingStage  string          `json:"pending_stage,omitempty"`
}

func (r *ToolRecoveryPublicationReport) fail(err error) {
	r.Partial, r.Error = true, toolReportError(err)
}

// PublishToolRecovery consumes a completed, quiescent merged tool-cache tree.
// The owner must exclude source writers and source-volume removal throughout
// this call (including overlay recovery/mount setup). A pin alone proves none
// of those facts. The original lower reader is retained through publication;
// only changed/new completed installs merge into the latest warm selection.
// Identical completed retries return the durable receipt without accessing the
// source or republishing. Publication never releases the recovery pin.
func PublishToolRecovery(ctx context.Context, root, source string, pin ToolRecoveryPin, maxBytes int64) ToolRecoveryPublicationReport {
	return publishToolRecovery(ctx, root, source, pin, maxBytes, syncToolRecoverySelection)
}
