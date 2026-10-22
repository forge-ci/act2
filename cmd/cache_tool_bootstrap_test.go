package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/nektos/act/pkg/artifactcache"
	"github.com/stretchr/testify/require"
)

func TestToolBootstrapCommandRequiresQuiescenceAndPreservesWarmth(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("immutable tool bootstrap requires Linux")
	}
	base := t.TempDir()
	source, store := filepath.Join(base, "legacy"), filepath.Join(base, "store")
	install := filepath.Join(source, "Go/1/x64")
	require.NoError(t, os.MkdirAll(install, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(install, "tool"), []byte("warm"), 0600))
	require.NoError(t, os.Chmod(filepath.Join(install, "tool"), 0755))
	require.NoError(t, os.WriteFile(install+".complete", nil, 0600))
	args := []string{"cache", "tool-bootstrap", "--from", source, "--cache-server-path", store, "--max-bytes", "100"}
	for _, flags := range [][]string{nil, {"--apply"}, {"--source-quiescent"}} {
		root := createRootCommand(context.Background(), &Input{}, "test")
		root.SetArgs(append(append([]string{}, args...), flags...))
		require.Error(t, root.Execute())
		_, err := os.Stat(store)
		require.True(t, os.IsNotExist(err))
	}
	root := createRootCommand(context.Background(), &Input{}, "test")
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetArgs(append(args, "--source-quiescent", "--apply"))
	require.NoError(t, root.Execute())
	var report artifactcache.ToolGenerationUpdateReport
	require.NoError(t, json.Unmarshal(output.Bytes(), &report))
	require.False(t, report.Partial, report.Error)
	require.True(t, report.Selected)
	require.True(t, report.Generation.Published)
	require.Len(t, report.Generation.ID, 64)
}
