package artifactcache

import "context"

// Enrollment creates a new empty source-protected store. It never upgrades an
// older store: existing helpers must be drained by an explicit migration.
type ToolRecoverySourceStoreReport struct {
	SchemaVersion int    `json:"schema_version"`
	Root          string `json:"root"`
	Enrolled      bool   `json:"enrolled"`
	Partial       bool   `json:"partial"`
	Error         string `json:"error,omitempty"`
}

func EnrollToolRecoverySourceStore(ctx context.Context, root string) ToolRecoverySourceStoreReport {
	report := ToolRecoverySourceStoreReport{SchemaVersion: 1, Root: root}
	if err := enrollToolRecoverySourceStore(ctx, root); err != nil {
		report.Partial, report.Error = true, toolReportError(err)
		return report
	}
	report.Enrolled = true
	return report
}

// This decision excludes source views, not engine writers. The caller proves
// exact source ownership and writer absence before physically deleting data.
type ToolRecoverySourceDisposalReport struct {
	SchemaVersion int             `json:"schema_version"`
	Root          string          `json:"root"`
	Pin           ToolRecoveryPin `json:"pin"`
	Disposed      bool            `json:"disposed"`
	Partial       bool            `json:"partial"`
	Error         string          `json:"error,omitempty"`
}

func FenceToolRecoverySourceDisposal(ctx context.Context, root string, pin ToolRecoveryPin) ToolRecoverySourceDisposalReport {
	report := ToolRecoverySourceDisposalReport{SchemaVersion: 1, Root: root, Pin: pin}
	if err := fenceToolRecoverySourceDisposal(ctx, root, pin); err != nil {
		report.Partial, report.Error = true, toolReportError(err)
		return report
	}
	report.Disposed = true
	return report
}
