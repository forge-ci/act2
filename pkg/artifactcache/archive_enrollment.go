package artifactcache

import (
	"context"
	"time"
)

// ArchiveEnrollmentReport separates persistent source fencing from publication.
// A fence does not stop processes with existing directory descriptors. Complete
// requires the caller to keep all such writers stopped until this call returns.
// Retained source data is never deleted by enrollment.
type ArchiveEnrollmentReport struct {
	SchemaVersion int    `json:"schema_version"`
	Source        string `json:"source"`
	Root          string `json:"root"`
	Namespace     string `json:"namespace"`
	Snapshot      string `json:"snapshot,omitempty"`
	MaxBytes      int64  `json:"max_bytes"`
	SourceFenced  bool   `json:"source_fenced"`
	Ready         bool   `json:"ready"`
	Partial       bool   `json:"partial"`
	Error         string `json:"error,omitempty"`
}

type ArchiveEnrollmentPhase uint8

const (
	ArchiveEnrollmentFence ArchiveEnrollmentPhase = iota
	ArchiveEnrollmentComplete
	ArchiveEnrollmentRequire
)

func EnrollArchive(ctx context.Context, source, root, namespace string, maxBytes int64, phase ArchiveEnrollmentPhase) ArchiveEnrollmentReport {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	report := ArchiveEnrollmentReport{SchemaVersion: 1, Source: source, Root: root, Namespace: namespace, MaxBytes: maxBytes}
	if err := enrollArchive(ctx, &report, phase); err != nil {
		report.Partial = true
		detail := []rune(err.Error())
		if len(detail) > 256 {
			detail = detail[:256]
		}
		report.Error = string(detail)
	}
	return report
}
