//go:build !linux

package artifactcache

import (
	"context"
	"fmt"
)

func bootstrapToolGeneration(_ context.Context, _, _ string, _ int64) (report ToolGenerationUpdateReport) {
	report.SchemaVersion = 1
	report.fail(fmt.Errorf("immutable tool bootstrap requires Linux"))
	return report
}
