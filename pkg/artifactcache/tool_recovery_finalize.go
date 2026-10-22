package artifactcache

import "context"

// A finalization receipt proves producer exclusion and reference absence, not
// engine writer absence, source ownership, helper teardown or physical deletion.
type ToolRecoveryFinalizationReport struct {
	SchemaVersion   int                     `json:"schema_version"`
	Root            string                  `json:"root"`
	Pin             ToolRecoveryPin         `json:"pin"`
	Publication     *ToolRecoveryCompletion `json:"publication,omitempty"`
	Disposed        bool                    `json:"disposed"`
	ReferenceAbsent bool                    `json:"reference_absent"`
	Partial         bool                    `json:"partial"`
	Error           string                  `json:"error,omitempty"`
}

func (r *ToolRecoveryFinalizationReport) fail(err error) {
	r.Partial, r.Error = true, toolReportError(err)
}

// FinalizeToolRecoverySource reconciles producer authority without an external
// registry. Before expiry it publishes and closes the exact original view before
// releasing protection. After expiry it never opens a new source view. Every
// retry revalidates the epoch and immutable identity and repeats the fence.
func FinalizeToolRecoverySource(ctx context.Context, root string, pin ToolRecoveryPin, maxBytes int64, openView ToolRecoverySourceFactory) ToolRecoveryFinalizationReport {
	return finalizeToolRecoverySource(ctx, root, pin, maxBytes, openView)
}
