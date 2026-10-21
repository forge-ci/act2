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

// ToolRecoverySourceView is an exclusively held source view. Close must unmount
// and dispose of the private view while the original lower reader is still held.
type ToolRecoverySourceView struct {
	Path  string
	Close func() error
}

// A factory that fails after allocating a view must return its Close handle
// with the error. Failed cleanup retains that handle and the original reader;
// subsequent calls retry cleanup before admitting any new view or receipt.
type ToolRecoverySourceFactory func(context.Context) (ToolRecoverySourceView, error)

// PublishToolRecoveryView opens a source only after admitting exact protection
// and acquiring/verifying its original lower reader. Completed retries do not
// open a view. This lets an overlay owner mount/recover under that same reader
// without a separate validation/hash pass or a gap in lower protection.
func PublishToolRecoveryView(ctx context.Context, root string, pin ToolRecoveryPin, maxBytes int64, openView ToolRecoverySourceFactory) ToolRecoveryPublicationReport {
	return publishToolRecoveryView(ctx, root, pin, maxBytes, openView, syncToolRecoverySelection)
}

func publishToolRecovery(ctx context.Context, root, source string, pin ToolRecoveryPin, maxBytes int64, syncSelection func(string) error) ToolRecoveryPublicationReport {
	return publishToolRecoveryView(ctx, root, pin, maxBytes, func(context.Context) (ToolRecoverySourceView, error) {
		return ToolRecoverySourceView{Path: source, Close: func() error { return nil }}, nil
	}, syncSelection)
}
