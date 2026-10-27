package artifactcache

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestToolOperationBudgetOverridesNestedDefaultAndRespectsParent(t *testing.T) {
	ctx, err := WithToolOperationBudget(context.Background(), 2*time.Second)
	require.NoError(t, err)
	started := time.Now()
	nested, cancel := toolOperationTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	deadline, ok := nested.Deadline()
	require.True(t, ok)
	require.GreaterOrEqual(t, deadline.Sub(started), 2*time.Second)

	parent, stop := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer stop()
	ctx, err = WithToolOperationBudget(parent, 2*time.Second)
	require.NoError(t, err)
	nested, cancel = toolOperationTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	deadline, ok = nested.Deadline()
	require.True(t, ok)
	parentDeadline, ok := parent.Deadline()
	require.True(t, ok)
	require.Equal(t, parentDeadline, deadline)
	for _, invalid := range []time.Duration{0, -time.Second, 12*time.Hour + time.Second} {
		_, err := WithToolOperationBudget(context.Background(), invalid)
		require.Error(t, err)
	}
}
