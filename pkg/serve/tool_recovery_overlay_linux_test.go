//go:build linux

package serve

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRecoveryOverlayMountRequiresExactReadOnlyMetacopy(t *testing.T) {
	paths := toolRecoveryOverlayPaths{lower: "/store/lower", upper: "/disk/upper", work: "/disk/work"}
	valid := "10 9 0:22 / /view ro,relatime - overlay overlay ro,lowerdir=/store/lower,upperdir=/disk/upper,workdir=/disk/work,metacopy=on"
	require.NoError(t, requireRecoveryOverlayMount(valid, "/view", paths))
	for name, bad := range map[string]string{
		"writable-vfs":     strings.Replace(valid, "ro,relatime", "rw,relatime", 1),
		"writable-super":   strings.Replace(valid, "overlay overlay ro,", "overlay overlay rw,", 1),
		"foreign-lower":    strings.Replace(valid, "lowerdir=/store/lower", "lowerdir=/foreign", 1),
		"foreign-upper":    strings.Replace(valid, "upperdir=/disk/upper", "upperdir=/foreign", 1),
		"foreign-work":     strings.Replace(valid, "workdir=/disk/work", "workdir=/foreign", 1),
		"no-metacopy":      strings.Replace(valid, ",metacopy=on", "", 1),
		"not-overlay":      strings.Replace(valid, "- overlay overlay", "- ext4 /dev/x", 1),
		"duplicate-target": valid + "\n" + valid,
		"duplicate-option": valid + ",lowerdir=/foreign",
		"conflicting-ro":   valid + ",rw",
		"truncated":        "10 9 0:22 / /view ro - overlay",
	} {
		t.Run(name, func(t *testing.T) { require.Error(t, requireRecoveryOverlayMount(bad, "/view", paths)) })
	}
}

func TestRecoveryOverlayPathsRefuseAliasingAndUnknownDiskLayout(t *testing.T) {
	root := t.TempDir()
	store, disk := filepath.Join(root, "store"), filepath.Join(root, "disk")
	lower := filepath.Join(store, ".tool-generations-v1", strings.Repeat("a", 64), "tree")
	for _, path := range []string{lower, filepath.Join(disk, nativeToolUpperName), filepath.Join(disk, nativeToolWorkName)} {
		require.NoError(t, os.MkdirAll(path, 0755))
	}
	request := ToolRecoveryOverlayRequest{Generation: ToolGeneration{Store: store}, SourceRoot: disk}
	request.Pin.Generation = strings.Repeat("a", 64)
	paths, err := recoveryOverlayPaths(request)
	require.NoError(t, err)
	require.Equal(t, lower, paths.lower)
	request.SourceRoot = filepath.Join(root, "missing")
	_, err = recoveryOverlayPaths(request)
	require.Error(t, err)
	request.SourceRoot = disk
	original := filepath.Join(disk, nativeToolUpperName)
	require.NoError(t, os.Rename(original, original+"-original"))
	require.NoError(t, os.Symlink(original+"-original", original))
	_, err = recoveryOverlayPaths(request)
	require.Error(t, err)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = openToolRecoveryOverlay(cancelled, request)
	require.ErrorIs(t, err, context.Canceled)
	require.True(t, recoveryPathsOverlap("/", store))
	require.True(t, recoveryPathsOverlap(store, filepath.Join(store, "child")))
	require.False(t, recoveryPathsOverlap(store, store+"-other"))
}
