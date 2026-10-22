//go:build !linux

package artifactcache

import "context"

// Coordinated archive enrollment is Linux-only; other cohorts have no sources.
func reclaimCohortArchiveSources(context.Context, string) error { return nil }

func archiveSourceReclamationPending(error) bool { return false }
