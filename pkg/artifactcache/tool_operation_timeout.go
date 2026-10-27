package artifactcache

import (
	"context"
	"fmt"
	"time"
)

type toolOperationBudgetKey struct{}

// WithToolOperationBudget opts a single enrollment or reservation operation
// into a finite budget. Parent cancellation and earlier deadlines still win.
func WithToolOperationBudget(ctx context.Context, budget time.Duration) (context.Context, error) {
	if budget < time.Second || budget > 12*time.Hour {
		return nil, fmt.Errorf("tool operation timeout must be between 1 second and 12 hours")
	}
	return context.WithValue(ctx, toolOperationBudgetKey{}, budget), nil
}

// Unconfigured callers keep their existing operation-specific deadline.
func toolOperationTimeout(ctx context.Context, fallback time.Duration) (context.Context, context.CancelFunc) {
	budget := fallback
	if configured, ok := ctx.Value(toolOperationBudgetKey{}).(time.Duration); ok {
		budget = configured
	}
	return context.WithTimeout(ctx, budget)
}
