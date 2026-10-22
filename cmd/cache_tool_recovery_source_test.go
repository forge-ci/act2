package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nektos/act/pkg/artifactcache"
	"github.com/stretchr/testify/require"
)

func TestToolRecoverySourceEnrollmentCommand(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux native store")
	}
	store := filepath.Join(t.TempDir(), "store")
	root := createRootCommand(context.Background(), &Input{}, "test")
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"cache", "tool-recovery", "enroll-source-store", "--cache-server-path", store, "--apply"})
	require.NoError(t, root.Execute(), "production CLI must establish the source-protected epoch before bootstrap")
	data, err := os.ReadFile(filepath.Join(store, ".tool-snapshot-store-v1"))
	require.NoError(t, err)
	require.Equal(t, "bosn-tool-snapshots-v3\n", string(data))
}

func TestToolRecoverySourceDisposalCommand(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux native store")
	}
	ctx := context.Background()
	base := t.TempDir()
	store := filepath.Join(base, "store")
	require.True(t, artifactcache.EnrollToolRecoverySourceStore(ctx, store).Enrolled)
	source := filepath.Join(base, "source")
	require.NoError(t, os.Mkdir(source, 0755))
	require.NoError(t, os.WriteFile(source+".complete", nil, 0600))
	require.NoError(t, os.WriteFile(filepath.Join(source, "payload"), []byte("warm"), 0600))
	object := artifactcache.PublishToolSnapshot(ctx, source, store, 100)
	require.False(t, object.Partial, object.Error)
	selected := artifactcache.InitializeToolGeneration(ctx, store, artifactcache.ToolGenerationSpec{SchemaVersion: 1, Installs: []artifactcache.ToolGenerationInstall{{Path: "Go/1/x64", ObjectID: object.ID}}}, 100)
	require.False(t, selected.Partial, selected.Error)
	now := time.Now().UTC()
	pin := artifactcache.ToolRecoveryPin{SchemaVersion: 1, Owner: strings.Repeat("a", 64), Generation: selected.Generation.ID, CreatedAt: now, ExpiresAt: now.Add(2 * time.Second)}
	reserved := artifactcache.PublishToolRecoveryPin(ctx, store, pin, 100)
	require.True(t, reserved.Published, reserved.Error)
	record := filepath.Join(base, "intent.json")
	data, err := json.Marshal(pin)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(record, data, 0600))
	run := func(apply bool) (artifactcache.ToolRecoverySourceDisposalReport, error) {
		root := createRootCommand(ctx, &Input{}, "test")
		var out bytes.Buffer
		root.SetOut(&out)
		args := []string{"cache", "tool-recovery", "dispose-source", "--cache-server-path", store, "--record", record}
		if apply {
			args = append(args, "--apply")
		}
		root.SetArgs(args)
		err := root.Execute()
		var report artifactcache.ToolRecoverySourceDisposalReport
		if out.Len() > 0 {
			require.NoError(t, json.Unmarshal(out.Bytes(), &report))
		}
		return report, err
	}
	_, err = run(false)
	require.ErrorContains(t, err, "--apply")
	refused, err := run(true)
	require.Error(t, err)
	require.True(t, refused.Partial)
	require.False(t, refused.Disposed)
	time.Sleep(time.Until(pin.ExpiresAt) + 20*time.Millisecond)
	disposed, err := run(true)
	require.NoError(t, err)
	require.True(t, disposed.Disposed)
	require.False(t, disposed.Partial)
	require.Equal(t, pin.Owner, disposed.Pin.Owner)
	retried, err := run(true)
	require.NoError(t, err)
	require.True(t, retried.Disposed)
}
