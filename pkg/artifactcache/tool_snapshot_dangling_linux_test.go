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

func TestToolSnapshotPreservesClosedMissingLibraryLeaf(t *testing.T) {
	source, store := completedToolFixture(t)
	for _, directory := range []string{"package/lib", "package/x86_64-conda-linux-gnu/lib"} {
		require.NoError(t, os.MkdirAll(filepath.Join(source, directory), 0755))
	}
	relative := "package/x86_64-conda-linux-gnu/lib/libgfortran.so"
	target := "../../lib/libgfortran.so"
	require.NoError(t, os.Symlink(target, filepath.Join(source, relative)))
	_, err := os.Stat(filepath.Join(source, relative))
	require.True(t, os.IsNotExist(err))
	report := PublishToolSnapshot(context.Background(), source, store, 100)
	require.False(t, report.Partial, report.Error)
	require.True(t, report.Published)
	copied := filepath.Join(report.Destination, "tree", relative)
	actual, err := os.Readlink(copied)
	require.NoError(t, err)
	require.Equal(t, target, actual)
	_, err = os.Stat(copied)
	require.True(t, os.IsNotExist(err), "preserve the missing target instead of repairing or dropping the link")
	payload, err := os.ReadFile(filepath.Join(report.Destination, "tree/tool"))
	require.NoError(t, err)
	require.Equal(t, "warm", string(payload))
	retry := PublishToolSnapshot(context.Background(), source, store, 100)
	require.False(t, retry.Partial, retry.Error)
	require.True(t, retry.Reused)
	require.Equal(t, report.ID, retry.ID)
}

func TestToolSnapshotMissingLinksStillRequireClosedParentsAndLeaf(t *testing.T) {
	for _, scenario := range []string{"transitive-dotdot", "missing-parent", "leaf-link-outside", "cycle"} {
		t.Run(scenario, func(t *testing.T) {
			source, store := completedToolFixture(t)
			var target string
			switch scenario {
			case "transitive-dotdot":
				require.NoError(t, os.Symlink(".", filepath.Join(source, "nested")))
				target = "nested/../not-present-outside"
			case "missing-parent":
				target = "not-present/leaf"
			case "leaf-link-outside":
				require.NoError(t, os.Symlink("../not-present-outside", filepath.Join(source, "leaf")))
				target = "leaf"
			case "cycle":
				require.NoError(t, os.Symlink("unresolved", filepath.Join(source, "loop")))
				target = "loop"
			}
			path := filepath.Join(source, "unresolved")
			require.NoError(t, os.Symlink(target, path))
			_, err := boundedToolSymlink(source, path, "unresolved")
			require.Error(t, err, "a missing target alone is not proof of closedness")
			report := PublishToolSnapshot(context.Background(), source, store, 100)
			require.True(t, report.Partial)
			require.False(t, report.Published)
			_, err = os.Stat(store)
			require.True(t, os.IsNotExist(err), "refusal must precede initializing the store")
		})
	}
}

func TestToolSnapshotPreservesClosedMissingLinkChain(t *testing.T) {
	source, store := completedToolFixture(t)
	require.NoError(t, os.MkdirAll(filepath.Join(source, "package/bin"), 0755))
	links := map[string]string{"clang++": "clang", "clang-cl": "clang", "clang": "clang-20"}
	for name, target := range links {
		require.NoError(t, os.Symlink(target, filepath.Join(source, "package/bin", name)))
	}
	report := PublishToolSnapshot(context.Background(), source, store, 100)
	require.False(t, report.Partial, report.Error)
	require.True(t, report.Published)
	for name, target := range links {
		copied := filepath.Join(report.Destination, "tree/package/bin", name)
		actual, err := os.Readlink(copied)
		require.NoError(t, err)
		require.Equal(t, target, actual)
		_, err = os.Stat(copied)
		require.True(t, os.IsNotExist(err), "copy the graph without synthesizing a tool binary")
	}
	retry := PublishToolSnapshot(context.Background(), source, store, 100)
	require.False(t, retry.Partial, retry.Error)
	require.True(t, retry.Reused)
	require.Equal(t, report.ID, retry.ID)
}

func TestToolMissingLinkChainCountsOriginalLink(t *testing.T) {
	for _, count := range []int{40, 41} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			source := t.TempDir()
			for index := 0; index < count; index++ {
				require.NoError(t, os.Symlink(fmt.Sprintf("link%d", index+1), filepath.Join(source, fmt.Sprintf("link%d", index))))
			}
			err := closedMissingToolLeaf(source, "link0", "link1")
			if count == 40 {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestToolMissingLinkChainRechecksEachRawParent(t *testing.T) {
	for _, target := range []string{"/not-present", "nested/../not-present"} {
		t.Run(target, func(t *testing.T) {
			source := t.TempDir()
			require.NoError(t, os.Symlink(".", filepath.Join(source, "nested")))
			require.NoError(t, os.Symlink(target, filepath.Join(source, "second")))
			require.Error(t, closedMissingToolLeaf(source, "first", "second"))
		})
	}
}

func TestToolMissingLinkChainCountsDirectoryLinks(t *testing.T) {
	source := t.TempDir()
	require.NoError(t, os.Symlink(".", filepath.Join(source, "alias")))
	for index := 0; index < 21; index++ {
		require.NoError(t, os.Symlink(fmt.Sprintf("alias/link%d", index+1), filepath.Join(source, fmt.Sprintf("link%d", index))))
	}
	// 21 leaf links + 21 directory aliases exceed the kernel's 40-link limit.
	require.Error(t, closedMissingToolLeaf(source, "link0", "alias/link1"))
}
