package artifactcache

import "context"

// ToolEnrollmentReport names a generation for subsequent reader admission.
// It is not a payload proof or reader lease: tool-exec must acquire its original
// reader and verify payload before starting the engine. Ready-store discovery
// avoids hashing payload a second time during planning.
type ToolEnrollmentReport struct {
	SchemaVersion int    `json:"schema_version"`
	Source        string `json:"source"`
	Root          string `json:"root"`
	Generation    string `json:"generation,omitempty"`
	Mode          string `json:"mode,omitempty"`
	Enrolled      bool   `json:"enrolled"`
	SourceFenced  bool   `json:"source_fenced"`
	Partial       bool   `json:"partial"`
	Error         string `json:"error,omitempty"`
}

// EnrollToolGeneration durably distinguishes initial enrollment from ordinary
// engine selection. The caller must exclude every legacy source writer for the
// complete call. The legacy path becomes a regular refusal file atomically;
// any existing tree is retained at the frozen private snapshot path. Missing
// warm state after enrollment is never bootstrapped.
func EnrollToolGeneration(ctx context.Context, source, root string, maxBytes int64) ToolEnrollmentReport {
	return enrollToolGeneration(ctx, source, root, maxBytes)
}

// FenceToolEnrollmentSource installs only the durable legacy refusal boundary.
// It admits no generation. Re-inventory and drain writers that could have opened
// the original directory before this cutover, then call CompleteToolEnrollment.
func FenceToolEnrollmentSource(ctx context.Context, source, root string, maxBytes int64) ToolEnrollmentReport {
	return enrollToolGenerationPhase(ctx, source, root, maxBytes, toolEnrollmentFenceOnly)
}

// CompleteToolEnrollment requires the existing exact source fence and never
// installs or repairs it. Call after proving old directory-FD writers absent.
func CompleteToolEnrollment(ctx context.Context, source, root string, maxBytes int64) ToolEnrollmentReport {
	return enrollToolGenerationPhase(ctx, source, root, maxBytes, toolEnrollmentRequireFenced)
}

type toolEnrollmentPhase uint8

const (
	toolEnrollmentAll toolEnrollmentPhase = iota
	toolEnrollmentFenceOnly
	toolEnrollmentRequireFenced
)

func (r *ToolEnrollmentReport) fail(err error) {
	r.Partial, r.Error = true, toolReportError(err)
}
