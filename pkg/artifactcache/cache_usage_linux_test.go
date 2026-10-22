//go:build linux

package artifactcache

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestCacheFootprintMatchesIndependentAllocationAcrossClasses(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"tools", "actions", "toolstore-v1", "unknown-stage"} {
		require.NoError(t, os.Mkdir(filepath.Join(root, name), 0755))
	}
	payload := filepath.Join(root, "tools", "payload")
	require.NoError(t, os.WriteFile(payload, bytes.Repeat([]byte("x"), 1<<20), 0600))
	require.NoError(t, os.Link(payload, filepath.Join(root, "actions", "same")))
	require.NoError(t, os.Link(payload, filepath.Join(root, "toolstore-v1", "same")))
	require.NoError(t, os.WriteFile(filepath.Join(root, "unknown-stage", "incomplete"), bytes.Repeat([]byte("x"), 4096), 0600))
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "payload"), bytes.Repeat([]byte("x"), 2<<20), 0600))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "actions", "outside-directory")))
	report := AuditCacheFootprint(context.Background(), root, 1000)
	require.False(t, report.Partial, report.Error)
	require.Equal(t, int64(2<<20), *report.Total.ReferencedFileBytes-*report.Total.UniqueFileBytes)
	require.Equal(t, int64((1<<20)+4096), *report.Total.UniqueFileBytes)
	// du is an independent inode/block oracle, including directories and links.
	// #nosec G204 -- Fixed executable/options over a private test root; no shell.
	output, err := exec.Command("du", "--summarize", "--block-size=1", root).Output()
	require.NoError(t, err)
	allocated, err := strconv.ParseInt(strings.Fields(string(output))[0], 10, 64)
	require.NoError(t, err)
	require.Equal(t, allocated, *report.Total.AllocatedBytes)
	classes := make(map[string]InodeUsage)
	for _, component := range report.Components {
		classes[component.Class] = component.Usage
	}
	for _, class := range []string{"tools", "actions", "toolcache"} {
		require.Equal(t, int64(1<<20), *classes[class].UniqueFileBytes)
	}
	require.Equal(t, int64(4096), *classes["other"].UniqueFileBytes, "unknown and incomplete state stays accounted")
	require.Zero(t, *classes["images"].AllocatedBytes, "an absent class is known empty after a complete scan")
}

func TestCacheFootprintIncompleteScansNeverPublishByteTotals(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "tools"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "tools", "payload"), []byte("owned"), 0600))
	symlink := filepath.Join(t.TempDir(), "root-link")
	require.NoError(t, os.Symlink(root, symlink))
	for _, scenario := range []string{"bound", "invalid-bound", "cancelled", "symlink-root", "symlink-slash", "symlink-dot", "symlink-parent", "filesystem-root", "relative-root", "missing-root", "depth"} {
		t.Run(scenario, func(t *testing.T) {
			path, bound := root, 1000
			ctx := context.Background()
			switch scenario {
			case "bound":
				bound = 2
			case "invalid-bound":
				bound = 0
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "symlink-root":
				path = symlink
			case "symlink-slash":
				path = symlink + "/"
			case "symlink-dot":
				path = symlink + "/."
			case "symlink-parent":
				path = filepath.Join(symlink, "tools")
			case "filesystem-root":
				path = "/"
			case "relative-root":
				path = "."
			case "missing-root":
				path = filepath.Join(root, "absent")
			case "depth":
				path = t.TempDir()
				nested := path
				for i := 0; i < 73; i++ {
					nested = filepath.Join(nested, "d")
					require.NoError(t, os.Mkdir(nested, 0755))
				}
			}
			report := AuditCacheFootprint(ctx, path, bound)
			require.True(t, report.Partial)
			require.NotEmpty(t, report.Error)
			require.Nil(t, report.Total.AllocatedBytes)
			require.Nil(t, report.Total.ApparentBytes)
			require.Nil(t, report.Total.UniqueFileBytes)
			require.Nil(t, report.Total.ReferencedFileBytes)
			for _, component := range report.Components {
				require.Nil(t, component.Usage.AllocatedBytes)
				require.Nil(t, component.Usage.UniqueFileBytes)
			}
		})
	}
}

// Run only inside a dedicated private privileged container, never on the host.
func TestCacheFootprintRefusesPrivateBindMounts(t *testing.T) {
	if os.Getenv("ACT_TEST_PRIVATE_MOUNTS") != "1" {
		t.Skip("requires dedicated private mount namespace")
	}
	for _, kind := range []string{"directory", "file"} {
		t.Run(kind, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(outside, "payload"), bytes.Repeat([]byte("x"), 1<<20), 0600))
			source, target := outside, filepath.Join(root, "mounted")
			if kind == "directory" {
				require.NoError(t, os.Mkdir(target, 0755))
			} else {
				source = filepath.Join(outside, "payload")
				require.NoError(t, os.WriteFile(target, nil, 0600))
			}
			require.NoError(t, unix.Mount(source, target, "", unix.MS_BIND, ""))
			defer func() { require.NoError(t, unix.Unmount(target, 0)) }()
			report := AuditCacheFootprint(context.Background(), root, 1000)
			require.True(t, report.Partial)
			require.Contains(t, report.Error, "mount boundary")
			require.Nil(t, report.Total.AllocatedBytes)
			require.Nil(t, report.Total.UniqueFileBytes)
		})
	}
}

func TestCacheFootprintBoundsNamespaceDetailsWithoutTruncatingTotal(t *testing.T) {
	root := t.TempDir()
	var largest string
	for i := 0; i < 257; i++ {
		namespace := fmt.Sprintf("%016x", i)
		directory := filepath.Join(root, "actcache", namespace)
		require.NoError(t, os.MkdirAll(directory, 0755))
		size := 1
		if i == 256 {
			size = 16384
			largest = "actcache/" + namespace
		}
		require.NoError(t, os.WriteFile(filepath.Join(directory, "payload"), bytes.Repeat([]byte("x"), size), 0600))
	}
	cohort := filepath.Join(root, "actcache", "cohort-v1", "0000000000000000")
	require.NoError(t, os.MkdirAll(cohort, 0755))
	require.NoError(t, os.Link(filepath.Join(root, "actcache", "0000000000000000", "payload"), filepath.Join(cohort, "shared")))
	require.NoError(t, os.WriteFile(filepath.Join(cohort, "extra"), bytes.Repeat([]byte("x"), 8192), 0600))
	report := AuditCacheFootprint(context.Background(), root, 10000)
	require.False(t, report.Partial, report.Error)
	require.Len(t, report.Namespaces, 256)
	require.Equal(t, 2, report.OmittedNamespaces)
	require.Equal(t, largest, report.Namespaces[0].StorePath)
	require.Equal(t, int64(256+16384+8192), *report.Total.UniqueFileBytes, "omitted detail must still contribute to full deduplicated total")
	require.Equal(t, int64(1), *report.Total.ReferencedFileBytes-*report.Total.UniqueFileBytes)
	require.Equal(t, "actcache/cohort-v1/0000000000000000", report.Namespaces[1].StorePath)
	require.Equal(t, "actcache/00000000000000fd", report.Namespaces[len(report.Namespaces)-1].StorePath, "equal allocations use deterministic path order")
}
