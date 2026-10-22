//go:build linux

package artifactcache

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func toolBootstrapFixture(t *testing.T) (string, string) {
	t.Helper()
	base := t.TempDir()
	source := filepath.Join(base, "legacy")
	for _, install := range []string{"Go/1/x64", "Node/24/x64", "uv/1/x64"} {
		path := filepath.Join(source, install)
		require.NoError(t, os.MkdirAll(path, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(path, "tool"), []byte(install), 0600))
		require.NoError(t, os.Chmod(filepath.Join(path, "tool"), 0755))
		if install != "uv/1/x64" {
			require.NoError(t, os.WriteFile(path+".complete", nil, 0600))
		}
	}
	return source, filepath.Join(base, "store")
}

func TestToolBootstrapPreservesCompletedWarmInstallsAndReconciles(t *testing.T) {
	source, root := toolBootstrapFixture(t)
	report := BootstrapToolGeneration(context.Background(), source, root, 100)
	require.False(t, report.Partial, report.Error)
	require.True(t, report.Selected)
	state, err := CurrentToolGenerationState(context.Background(), root, 100)
	require.NoError(t, err)
	require.Len(t, state.Installs, 2, "incomplete install must stay out of the warm generation")
	for _, install := range state.Installs {
		payload := filepath.Join(root, toolGenerationDirectory, state.ID, "tree", install.Path, "tool")
		data, err := os.ReadFile(payload)
		require.NoError(t, err)
		require.Equal(t, install.Path, string(data))
		info, err := os.Stat(payload)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0755), info.Mode().Perm())
	}
	require.NoError(t, os.Rename(source, source+".gone"))
	repeat := BootstrapToolGeneration(context.Background(), source, root, 100)
	require.False(t, repeat.Partial, repeat.Error)
	require.True(t, repeat.Selected)
	require.True(t, repeat.Generation.Reused)
	require.Equal(t, state.ID, repeat.Generation.ID)
}

func TestToolBootstrapRefusesMissingEmptyOverBudgetAndCorruptWarmState(t *testing.T) {
	for _, scenario := range []string{"missing", "empty", "budget", "cancelled", "corrupt-selection", "corrupt-payload"} {
		t.Run(scenario, func(t *testing.T) {
			source, root := toolBootstrapFixture(t)
			ctx := context.Background()
			limit := int64(100)
			existing := false
			switch scenario {
			case "missing":
				source += ".absent"
			case "empty":
				source = t.TempDir()
			case "budget":
				limit = 1
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "corrupt-selection", "corrupt-payload":
				first := BootstrapToolGeneration(ctx, source, root, limit)
				require.False(t, first.Partial, first.Error)
				existing = true
				if scenario == "corrupt-selection" {
					require.NoError(t, os.WriteFile(filepath.Join(root, toolGenerationCurrent), []byte("broken"), 0600))
				} else {
					require.NoError(t, os.WriteFile(filepath.Join(first.Generation.Destination, "tree", "Go/1/x64/tool"), []byte("corrupt"), 0600))
				}
			}
			report := BootstrapToolGeneration(ctx, source, root, limit)
			require.True(t, report.Partial)
			require.False(t, report.Selected)
			if !existing {
				_, err := os.Stat(root)
				require.True(t, os.IsNotExist(err), "rejected input initialized the store")
			}
		})
	}
}

func TestToolBootstrapReconcilesSelectionSyncFailureWithoutReimport(t *testing.T) {
	source, root := toolBootstrapFixture(t)
	report := bootstrapToolGenerationWithSelectionSync(context.Background(), source, root, 100, func(string) error { return fmt.Errorf("injected directory sync failure") })
	require.True(t, report.Partial)
	require.True(t, report.Selected, "visible selection must remain disclosed on lost durability acknowledgement")
	require.NoError(t, os.Rename(source, source+".gone"))
	retry := BootstrapToolGeneration(context.Background(), source, root, 100)
	require.False(t, retry.Partial, retry.Error)
	require.True(t, retry.Selected)
	require.True(t, retry.Generation.Reused)
	require.Equal(t, report.Generation.ID, retry.Generation.ID)
}

func TestToolBootstrapPreservesForeignStoreAndRejectsAliasedAncestry(t *testing.T) {
	for _, scenario := range []string{"foreign", "root-link", "parent-link", "zero-limit"} {
		t.Run(scenario, func(t *testing.T) {
			source, root := toolBootstrapFixture(t)
			limit := int64(100)
			switch scenario {
			case "foreign":
				require.NoError(t, os.Mkdir(root, 0755))
				require.NoError(t, os.WriteFile(filepath.Join(root, "foreign"), []byte("keep"), 0600))
			case "root-link":
				require.NoError(t, os.Symlink(t.TempDir(), root))
			case "parent-link":
				alias := filepath.Join(t.TempDir(), "alias")
				require.NoError(t, os.Symlink(filepath.Dir(root), alias))
				root = filepath.Join(alias, "store")
			case "zero-limit":
				limit = 0
			}
			report := BootstrapToolGeneration(context.Background(), source, root, limit)
			require.True(t, report.Partial)
			require.False(t, report.Selected)
			if scenario == "foreign" {
				data, err := os.ReadFile(filepath.Join(root, "foreign"))
				require.NoError(t, err)
				require.Equal(t, "keep", string(data))
			}
		})
	}
}

func TestToolBootstrapRefusesAliasedCompletedNamespaceBeforeMutation(t *testing.T) {
	source, root := toolBootstrapFixture(t)
	require.NoError(t, os.Symlink(filepath.Join(source, "Go"), filepath.Join(source, "GoAlias")))
	report := BootstrapToolGeneration(context.Background(), source, root, 100)
	require.True(t, report.Partial, "a linked warm namespace must not be silently omitted")
	require.False(t, report.Selected)
	_, err := os.Stat(root)
	require.True(t, os.IsNotExist(err))
}

func TestToolBootstrapAcceptsDeliberatelyPreparedEmptyDestination(t *testing.T) {
	source, root := toolBootstrapFixture(t)
	require.NoError(t, os.Mkdir(root, 0755))
	report := BootstrapToolGeneration(context.Background(), source, root, 100)
	require.False(t, report.Partial, report.Error)
	require.True(t, report.Selected)
}

func TestToolBootstrapExcludesPrivateLegacyRootEntries(t *testing.T) {
	for _, scenario := range []string{"stage", "oversized-stage", "alias"} {
		t.Run(scenario, func(t *testing.T) {
			source, root := toolBootstrapFixture(t)
			if scenario != "alias" {
				stage := filepath.Join(source, ".saving-abandoned", "install")
				require.NoError(t, os.MkdirAll(stage, 0755))
				payload := []byte("unpublished")
				if scenario == "oversized-stage" {
					payload = make([]byte, 200)
				}
				require.NoError(t, os.WriteFile(filepath.Join(stage, "tool"), payload, 0600))
				require.NoError(t, os.WriteFile(filepath.Join(stage, ".complete"), nil, 0600))
			} else {
				require.NoError(t, os.Symlink(filepath.Join(source, "Go"), filepath.Join(source, ".private-alias")))
			}
			report := BootstrapToolGeneration(context.Background(), source, root, 100)
			require.False(t, report.Partial, report.Error)
			state, err := CurrentToolGenerationState(context.Background(), root, 100)
			require.NoError(t, err)
			require.Len(t, state.Installs, 2, "private root entries are not part of the published legacy tool cache")
			for _, install := range state.Installs {
				require.NotContains(t, install.Path, ".saving-")
			}
		})
	}
}
