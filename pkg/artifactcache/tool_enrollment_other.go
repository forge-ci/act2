//go:build !linux

package artifactcache

import (
	"context"
	"fmt"
)

func enrollToolGeneration(_ context.Context, source, root string, _ int64) ToolEnrollmentReport {
	report := ToolEnrollmentReport{SchemaVersion: 1, Source: source, Root: root}
	report.fail(fmt.Errorf("tool enrollment requires Linux"))
	return report
}

func enrollToolGenerationPhase(ctx context.Context, source, root string, maxBytes int64, _ toolEnrollmentPhase) ToolEnrollmentReport {
	return enrollToolGeneration(ctx, source, root, maxBytes)
}
