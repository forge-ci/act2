//go:build linux

package artifactcache

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExplicitEmptyToolGenerationCanBeSelectedAndLeased(t *testing.T) {
	root := t.TempDir()
	spec := ToolGenerationSpec{SchemaVersion: 1, Installs: []ToolGenerationInstall{}}
	// An explicit desired empty generation is a cold initial state. Missing
	// current selection must still fail before the caller chooses to publish.
	_, err := CurrentToolGenerationState(context.Background(), root, 100)
	require.Error(t, err)
	report := PublishToolGeneration(context.Background(), root, spec, 100)
	require.False(t, report.Partial, report.Error)
	require.True(t, report.Published)
	require.EqualValues(t, 0, *report.Bytes)
	require.Equal(t, 1, *report.Entries, "the closed tree contains only its root")
	selected := InitializeToolGeneration(context.Background(), root, spec, 100)
	require.False(t, selected.Partial, selected.Error)
	require.True(t, selected.Selected)
	state, err := CurrentToolGenerationState(context.Background(), root, 100)
	require.NoError(t, err)
	require.Equal(t, report.ID, state.ID)
	require.NotNil(t, state.Installs, "encode an explicit array rather than null")
	require.Empty(t, state.Installs)
	data, err := json.Marshal(state)
	require.NoError(t, err)
	require.Contains(t, string(data), `"installs":[]`)
	reader, err := AcquireToolGenerationLease(context.Background(), root, state.ID, 100)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	// A corrupt closed empty tree must not become a valid empty fallback.
	require.NoError(t, os.WriteFile(filepath.Join(report.Destination, "tree", "unexpected"), nil, 0600))
	_, err = CurrentToolGenerationState(context.Background(), root, 100)
	require.Error(t, err)
}

func TestEmptyToolGenerationAcceptsFirstCompletedInstall(t *testing.T) {
	source, root := completedToolFixture(t)
	object := PublishToolSnapshot(context.Background(), source, root, 100)
	require.False(t, object.Partial, object.Error)
	empty := ToolGenerationSpec{SchemaVersion: 1, Installs: []ToolGenerationInstall{}}
	cold := InitializeToolGeneration(context.Background(), root, empty, 100)
	require.False(t, cold.Partial, cold.Error)
	require.True(t, cold.Selected)
	reader, err := AcquireToolGenerationLease(context.Background(), root, cold.Generation.ID, 100)
	require.NoError(t, err)
	defer reader.Close()
	updates := ToolGenerationSpec{SchemaVersion: 1, Installs: []ToolGenerationInstall{{Path: "Go/1/x64", ObjectID: object.ID}}}
	warm := UpdateToolGeneration(context.Background(), root, updates, 100)
	require.False(t, warm.Partial, warm.Error)
	require.True(t, warm.Selected)
	require.NotEqual(t, cold.Generation.ID, warm.Generation.ID)
	state, err := CurrentToolGenerationState(context.Background(), root, 100)
	require.NoError(t, err)
	require.Equal(t, updates.Installs, state.Installs)
	payload, err := os.ReadFile(filepath.Join(warm.Generation.Destination, "tree/Go/1/x64/tool"))
	require.NoError(t, err)
	require.Equal(t, "warm", string(payload))
	noop := UpdateToolGeneration(context.Background(), root, empty, 100)
	require.False(t, noop.Partial, noop.Error)
	require.True(t, noop.Selected)
	require.Equal(t, warm.Generation.ID, noop.Generation.ID, "an empty merge must preserve completed installs")
	// The old immutable empty lower remains valid until its reader closes.
	second, err := AcquireToolGenerationLease(context.Background(), root, cold.Generation.ID, 100)
	require.NoError(t, err)
	require.NoError(t, second.Close())
}
