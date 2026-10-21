//go:build linux

package artifactcache

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func toolRecoveryPublicationFixture(t *testing.T) (string, string, ToolRecoveryPin) {
	t.Helper()
	ctx := context.Background()
	root, spec := toolGenerationFixture(t)
	selected := InitializeToolGeneration(ctx, root, spec, 100)
	require.False(t, selected.Partial, selected.Error)
	now := time.Now().UTC()
	pin := ToolRecoveryPin{1, strings.Repeat("a", 64), selected.Generation.ID, now, now.Add(time.Hour)}
	reserved := PublishToolRecoveryPin(ctx, root, pin, 100)
	require.False(t, reserved.Partial, reserved.Error)
	source := filepath.Join(t.TempDir(), "merged")
	require.NoError(t, os.Mkdir(source, 0755))
	lower := filepath.Join(root, toolGenerationDirectory, pin.Generation, "tree")
	require.NoError(t, exec.Command("cp", "-a", lower+"/.", source).Run()) //nolint:gosec // test-owned fixture paths
	return root, source, pin
}

func TestToolRecoveryPublicationReconcilesAnUncertainSelection(t *testing.T) {
	root, source, pin := toolRecoveryPublicationFixture(t)
	failed := publishToolRecovery(context.Background(), root, source, pin, 100, func(string) error { return errors.New("injected selection sync failure") })
	require.True(t, failed.Partial)
	require.False(t, failed.Completed)
	require.NotEmpty(t, failed.PendingStage)
	pointer, err := readToolGenerationSelection(root)
	require.NoError(t, err)
	require.NotNil(t, pointer.Recovery, "completion and selected generation share one atomic pointer")
	require.NoError(t, os.Rename(source, source+"-unmounted"))
	retried := PublishToolRecovery(context.Background(), root, source, pin, 100)
	require.False(t, retried.Partial, retried.Error)
	require.True(t, retried.Completed)
	require.Equal(t, failed.Generation, retried.Generation)
	require.True(t, failed.CompletedAt.Equal(retried.CompletedAt))
	require.Empty(t, retried.PendingStage)
	// A later update must archive the receipt before it can replace the pointer.
	spec := ToolGenerationSpec{SchemaVersion: 1, Installs: []ToolGenerationInstall{{Path: "Node/2/x64", ObjectID: strings.Repeat("0", 64)}}}
	changed := UpdateToolGeneration(context.Background(), root, spec, 100)
	require.True(t, changed.Partial, "missing objects still refuse after receipt archival")
	receipt, err := readToolRecoveryCompletion(filepath.Join(root, toolRecoveryCompletionDirectory, pin.Owner+".json"))
	require.NoError(t, err)
	require.Equal(t, retried.Generation, receipt.Generation)
}

func TestToolRecoveryPublicationRefusesChangedIntentAndUnprotectedSources(t *testing.T) {
	for _, scenario := range []string{"changed-expiry", "absent-pin", "cancelled", "missing-source", "corrupt-lower", "corrupt-selection"} {
		t.Run(scenario, func(t *testing.T) {
			root, source, pin := toolRecoveryPublicationFixture(t)
			ctx := context.Background()
			switch scenario {
			case "changed-expiry":
				pin.ExpiresAt = pin.ExpiresAt.Add(time.Minute)
			case "absent-pin":
				released := ReleaseToolRecoveryPin(ctx, root, pin)
				require.False(t, released.Partial, released.Error)
			case "cancelled":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			case "missing-source":
				source += "-missing"
			case "corrupt-lower":
				require.NoError(t, os.WriteFile(filepath.Join(root, toolGenerationDirectory, pin.Generation, "manifest.json"), []byte("bad"), 0600))
			case "corrupt-selection":
				require.NoError(t, os.WriteFile(filepath.Join(root, toolGenerationCurrent), []byte("bad"), 0600))
			}
			before, err := os.ReadFile(filepath.Join(root, toolGenerationCurrent))
			require.NoError(t, err)
			report := PublishToolRecovery(ctx, root, source, pin, 100)
			require.True(t, report.Partial)
			require.False(t, report.Completed)
			after, err := os.ReadFile(filepath.Join(root, toolGenerationCurrent))
			require.NoError(t, err)
			require.Equal(t, before, after, "refused recovery cannot replace selection")
		})
	}
}

func TestToolRecoveryCompletionConflictsStopSuccessors(t *testing.T) {
	root, source, pin := toolRecoveryPublicationFixture(t)
	report := PublishToolRecovery(context.Background(), root, source, pin, 100)
	require.False(t, report.Partial, report.Error)
	changed := pin
	changed.ExpiresAt = changed.ExpiresAt.Add(time.Minute)
	refused := PublishToolRecovery(context.Background(), root, source, changed, 100)
	require.True(t, refused.Partial)
	require.Contains(t, refused.Error, "differs")
	directory := filepath.Join(root, toolRecoveryCompletionDirectory)
	require.NoError(t, os.Mkdir(directory, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(directory, pin.Owner+".json"), []byte("corrupt"), 0600))
	pointer, err := os.ReadFile(filepath.Join(root, toolGenerationCurrent))
	require.NoError(t, err)
	update := UpdateToolGeneration(context.Background(), root, ToolGenerationSpec{SchemaVersion: 1, Installs: []ToolGenerationInstall{{Path: "invalid", ObjectID: strings.Repeat("0", 64)}}}, 100)
	require.True(t, update.Partial)
	require.False(t, update.Selected)
	after, err := os.ReadFile(filepath.Join(root, toolGenerationCurrent))
	require.NoError(t, err)
	require.Equal(t, pointer, after, "an unarchivable receipt cannot be discarded")
}

func TestToolRecoveryScanningBoundsAndCompletionConventions(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{"Go/1/x64", "nested/tool/version/slug", "uv/1/x64"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, path), 0755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "Go/1/x64.complete"), nil, 0600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "nested/tool/version/slug/.complete"), nil, 0600))
	paths, err := completedToolRecoveryPaths(context.Background(), root)
	require.NoError(t, err)
	require.Equal(t, []string{"Go/1/x64", "nested/tool/version/slug"}, paths)
	require.NoError(t, os.Symlink("missing", filepath.Join(root, "uv/1/x64.complete")))
	_, err = completedToolRecoveryPaths(context.Background(), root)
	require.ErrorContains(t, err, "marker is not regular")
}
