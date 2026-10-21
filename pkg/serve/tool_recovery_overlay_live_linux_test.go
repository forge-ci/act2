//go:build linux

package serve

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nektos/act/pkg/artifactcache"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// Run inside a dedicated privileged Linux container, with TMPDIR on its private
// disk volume. Ordinary host test runners cannot create overlay mounts.
func TestToolRecoveryOverlayLiveStoppedDisk(t *testing.T) {
	if os.Getenv("ACT_TEST_OVERLAY_RECOVERY") != "1" {
		t.Skip("requires dedicated privileged recovery fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	root := t.TempDir()
	source, store, disk, active := filepath.Join(root, "install"), filepath.Join(root, "store"), filepath.Join(root, "disk"), filepath.Join(root, "active")
	for _, path := range []string{source, active, filepath.Join(disk, nativeToolUpperName), filepath.Join(disk, nativeToolWorkName)} {
		require.NoError(t, os.MkdirAll(path, 0755))
	}
	payload := make([]byte, 16<<20)
	_, err := rand.Read(payload)
	require.NoError(t, err)
	write(t, source+".complete", "")
	require.NoError(t, os.WriteFile(filepath.Join(source, "payload"), payload, 0644)) //nolint:gosec // fixture deliberately changes 0644 to 0600 through overlay
	write(t, filepath.Join(source, "settings"), "original")
	object := artifactcache.PublishToolSnapshot(ctx, source, store, 64<<20)
	require.False(t, object.Partial, object.Error)
	initial := artifactcache.InitializeToolGeneration(ctx, store, artifactcache.ToolGenerationSpec{SchemaVersion: 1, Installs: []artifactcache.ToolGenerationInstall{{Path: "Go/1/x64", ObjectID: object.ID}, {Path: "Node/24/x64", ObjectID: object.ID}}}, 64<<20)
	require.False(t, initial.Partial, initial.Error)
	now := time.Now().UTC()
	pin := artifactcache.ToolRecoveryPin{SchemaVersion: 1, Owner: strings.Repeat("a", 64), Generation: initial.Generation.ID, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	reserved := artifactcache.PublishToolRecoveryPin(ctx, store, pin, 64<<20)
	require.False(t, reserved.Partial, reserved.Error)
	reader, err := artifactcache.AcquireToolGenerationLease(ctx, store, pin.Generation, 64<<20)
	require.NoError(t, err)
	defer func() {
		if reader != nil {
			_ = reader.Close()
		}
	}()
	lower := filepath.Join(store, ".tool-generations-v1", pin.Generation, "tree")
	upper, work := filepath.Join(disk, nativeToolUpperName), filepath.Join(disk, nativeToolWorkName)
	options := "metacopy=on,lowerdir=" + lower + ",upperdir=" + upper + ",workdir=" + work
	require.NoError(t, unix.Mount("overlay", active, "overlay", 0, options))
	mounted := true
	defer func() {
		if mounted {
			require.NoError(t, unix.Unmount(active, 0))
		}
	}()
	require.NoError(t, os.Chmod(filepath.Join(active, "Go/1/x64/payload"), 0600))
	write(t, filepath.Join(active, "Go/1/x64/settings"), "recovered")
	require.NoError(t, os.MkdirAll(filepath.Join(active, "uv/1/x64"), 0755))
	write(t, filepath.Join(active, "uv/1/x64/incomplete"), "not completed")
	upperPayload := filepath.Join(upper, "Go/1/x64/payload")
	_, err = unix.Getxattr(upperPayload, "trusted.overlay.metacopy", nil)
	require.NoError(t, err, "kernel must retain the original metacopy lower")
	info, err := os.Stat(upperPayload)
	require.NoError(t, err)
	allocated := info.Sys().(*syscall.Stat_t).Blocks * 512
	require.Less(t, allocated, int64(len(payload)), "metadata-only upper must not contain a full payload copy")
	t.Logf("metadata-only upper: logical=%d allocated=%d", info.Size(), allocated)
	require.NoError(t, unix.Unmount(active, 0))
	mounted = false
	require.NoError(t, reader.Close())
	reader = nil
	// A different engine has already completed a newer Node install.
	write(t, filepath.Join(source, "payload"), "newer-node")
	newer := artifactcache.PublishToolSnapshot(ctx, source, store, 64<<20)
	require.False(t, newer.Partial, newer.Error)
	changed := artifactcache.UpdateToolGeneration(ctx, store, artifactcache.ToolGenerationSpec{SchemaVersion: 1, Installs: []artifactcache.ToolGenerationInstall{{Path: "Node/24/x64", ObjectID: newer.ID}}}, 64<<20)
	require.False(t, changed.Partial, changed.Error)
	require.Error(t, artifactcache.RetireToolGeneration(ctx, store, pin.Generation, 64<<20), "pin protects stopped original lower")
	before, err := filepath.Glob("/tmp/act-tool-recovery-*")
	require.NoError(t, err)
	request := ToolRecoveryOverlayRequest{Generation: ToolGeneration{ID: pin.Generation, Store: store, OverlayRecipeSHA256: digestOf([]byte(OverlayRecipe(store, "/opt/hostedtoolcache")))}, Pin: pin, SourceRoot: disk, ToolCacheMount: "/opt/hostedtoolcache", MaxBytes: 64 << 20}
	report := RecoverToolCache(ctx, request)
	require.False(t, report.Partial, report.Error)
	require.True(t, report.Completed)
	after, err := filepath.Glob("/tmp/act-tool-recovery-*")
	require.NoError(t, err)
	require.Equal(t, before, after, "private recovered mount and directory are removed")
	tree := filepath.Join(store, ".tool-generations-v1", report.Generation, "tree")
	recovered, err := os.ReadFile(filepath.Join(tree, "Go/1/x64/payload"))
	require.NoError(t, err)
	require.Equal(t, sha256.Sum256(payload), sha256.Sum256(recovered))
	recoveredInfo, err := os.Stat(filepath.Join(tree, "Go/1/x64/payload"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), recoveredInfo.Mode().Perm())
	settings, err := os.ReadFile(filepath.Join(tree, "Go/1/x64/settings"))
	require.NoError(t, err)
	require.Equal(t, "recovered", string(settings))
	node, err := os.ReadFile(filepath.Join(tree, "Node/24/x64/payload"))
	require.NoError(t, err)
	require.Equal(t, "newer-node", string(node))
	require.NoDirExists(t, filepath.Join(tree, "uv"))
	original, err := os.ReadFile(filepath.Join(lower, "Go/1/x64/settings"))
	require.NoError(t, err)
	require.True(t, bytes.Equal([]byte("original"), original), "original lower is unchanged")
	require.Error(t, artifactcache.RetireToolGeneration(ctx, store, pin.Generation, 64<<20), "publication does not release the pin")
	// Reconciliation must not need the stopped source after publication.
	require.NoError(t, os.Rename(disk, disk+"-unmounted"))
	require.Equal(t, report, RecoverToolCache(ctx, request))
	released := artifactcache.ReleaseToolRecoveryPin(ctx, store, pin)
	require.False(t, released.Partial, released.Error)
	require.NoError(t, artifactcache.RetireToolGeneration(ctx, store, pin.Generation, 64<<20))
}
