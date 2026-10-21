//go:build linux

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nektos/act/pkg/artifactcache"
	"github.com/stretchr/testify/require"
)

func TestToolRecoveryPublishPreservesNewerInstallsAndReconcilesLostReply(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	source, store := filepath.Join(base, "install"), filepath.Join(base, "store")
	require.NoError(t, os.Mkdir(source, 0755))
	require.NoError(t, os.WriteFile(source+".complete", nil, 0600))
	require.NoError(t, os.WriteFile(filepath.Join(source, "payload"), []byte("original"), 0600))
	object := artifactcache.PublishToolSnapshot(ctx, source, store, 10000)
	require.False(t, object.Partial, object.Error)
	spec := artifactcache.ToolGenerationSpec{SchemaVersion: 1, Installs: []artifactcache.ToolGenerationInstall{
		{Path: "Go/1/x64", ObjectID: object.ID}, {Path: "Node/24/x64", ObjectID: object.ID},
	}}
	initial := artifactcache.InitializeToolGeneration(ctx, store, spec, 10000)
	require.False(t, initial.Partial, initial.Error)
	now := time.Now().UTC()
	pin := artifactcache.ToolRecoveryPin{SchemaVersion: 1, Owner: strings.Repeat("a", 64), Generation: initial.Generation.ID, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	reserved := artifactcache.PublishToolRecoveryPin(ctx, store, pin, 10000)
	require.False(t, reserved.Partial, reserved.Error)
	record := filepath.Join(base, "intent.json")
	data, err := json.Marshal(pin)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(record, data, 0600))
	merged := filepath.Join(base, "merged")
	require.NoError(t, os.Mkdir(merged, 0755))
	lower := filepath.Join(store, ".tool-generations-v1", initial.Generation.ID, "tree")
	require.NoError(t, exec.Command("cp", "-a", lower+"/.", merged).Run()) //nolint:gosec // test-owned fixture paths
	require.NoError(t, os.WriteFile(filepath.Join(merged, "Go/1/x64/settings"), []byte("recovered"), 0600))
	require.NoError(t, os.MkdirAll(filepath.Join(merged, "uv/1/x64"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(merged, "uv/1/x64/incomplete"), []byte("no marker"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(source, "payload"), []byte("newer-node"), 0600))
	newer := artifactcache.PublishToolSnapshot(ctx, source, store, 10000)
	require.False(t, newer.Partial, newer.Error)
	update := artifactcache.UpdateToolGeneration(ctx, store, artifactcache.ToolGenerationSpec{SchemaVersion: 1, Installs: []artifactcache.ToolGenerationInstall{{Path: "Node/24/x64", ObjectID: newer.ID}}}, 10000)
	require.False(t, update.Partial, update.Error)
	run := func() ([]byte, error) {
		root := createRootCommand(ctx, &Input{}, "test")
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetArgs([]string{"cache", "tool-recovery", "publish", "--cache-server-path", store, "--record", record, "--source", merged, "--max-bytes", "10000", "--apply", "--source-quiescent"})
		err := root.Execute()
		return out.Bytes(), err
	}
	out, err := run()
	require.NoError(t, err)
	var report struct {
		Completed   bool      `json:"completed"`
		Generation  string    `json:"generation"`
		CompletedAt time.Time `json:"completed_at"`
		Partial     bool      `json:"partial"`
	}
	require.NoError(t, json.Unmarshal(out, &report))
	require.True(t, report.Completed)
	require.False(t, report.Partial)
	require.False(t, report.CompletedAt.IsZero())
	tree := filepath.Join(store, ".tool-generations-v1", report.Generation, "tree")
	settings, err := os.ReadFile(filepath.Join(tree, "Go/1/x64/settings"))
	require.NoError(t, err)
	require.Equal(t, "recovered", string(settings))
	node, err := os.ReadFile(filepath.Join(tree, "Node/24/x64/payload"))
	require.NoError(t, err)
	require.Equal(t, "newer-node", string(node), "unchanged old lower must not overwrite a newer completed install")
	require.NoDirExists(t, filepath.Join(tree, "uv"))
	// Simulate a lost reply followed by a newer publication, with no source left
	// at its old mount. The retry must return its durable receipt, not republish.
	require.NoError(t, os.Rename(merged, merged+"-unmounted"))
	later := artifactcache.UpdateToolGeneration(ctx, store, artifactcache.ToolGenerationSpec{SchemaVersion: 1, Installs: []artifactcache.ToolGenerationInstall{{Path: "Go/1/x64", ObjectID: newer.ID}}}, 10000)
	require.False(t, later.Partial, later.Error)
	retried, err := run()
	require.NoError(t, err)
	require.JSONEq(t, string(out), string(retried))
	current, err := artifactcache.CurrentToolGeneration(ctx, store, 10000)
	require.NoError(t, err)
	require.Equal(t, later.Generation.ID, current.ID)
	released := artifactcache.ReleaseToolRecoveryPin(ctx, store, pin)
	require.False(t, released.Partial, released.Error)
	require.True(t, released.Removed, "publication does not release source protection")
}

func TestToolRecoveryPublishRequiresExplicitSourceQuiescence(t *testing.T) {
	root := createRootCommand(context.Background(), &Input{}, "test")
	root.SetArgs([]string{"cache", "tool-recovery", "publish", "--apply"})
	require.ErrorContains(t, root.Execute(), "--source-quiescent")
}
