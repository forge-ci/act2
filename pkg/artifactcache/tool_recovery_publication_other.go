//go:build !linux

package artifactcache

import (
	"context"
	"fmt"
)

func syncToolRecoverySelection(string) error { return fmt.Errorf("tool recovery requires Linux") }
func publishToolRecovery(_ context.Context, root, _ string, pin ToolRecoveryPin, _ int64, _ func(string) error) ToolRecoveryPublicationReport {
	r := ToolRecoveryPublicationReport{SchemaVersion: 1, Root: root, Pin: pin}
	r.fail(fmt.Errorf("tool recovery requires Linux"))
	return r
}
