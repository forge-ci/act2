//go:build linux

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCacheUsageCommandDeduplicatesClassesAndBoundsInventory(t *testing.T) {
	rootPath := t.TempDir()
	for _, name := range []string{"tools", "actions", "actcache"} {
		require.NoError(t, os.Mkdir(filepath.Join(rootPath, name), 0755))
	}
	payload := filepath.Join(rootPath, "tools", "payload")
	require.NoError(t, os.WriteFile(payload, bytes.Repeat([]byte("a"), 1<<20), 0600))
	require.NoError(t, os.Link(payload, filepath.Join(rootPath, "actions", "shared")))
	sparse := filepath.Join(rootPath, "actcache", "sparse")
	require.NoError(t, os.WriteFile(sparse, nil, 0600))
	require.NoError(t, os.Truncate(sparse, 16<<20))
	outside := filepath.Join(t.TempDir(), "outside")
	require.NoError(t, os.WriteFile(outside, bytes.Repeat([]byte("x"), 2<<20), 0600))
	require.NoError(t, os.Symlink(outside, filepath.Join(rootPath, "actions", "outside")))
	for _, bound := range []string{"1000", "1"} {
		command := createRootCommand(context.Background(), &Input{}, "test")
		var out bytes.Buffer
		command.SetOut(&out)
		command.SetArgs([]string{"cache", "usage", "--cache-server-path", rootPath, "--max-entries", bound})
		err := command.Execute()
		var report struct {
			SchemaVersion int  `json:"schema_version"`
			Partial       bool `json:"partial"`
			Total         struct {
				Allocated  *int64 `json:"allocated_bytes"`
				Unique     *int64 `json:"unique_file_bytes"`
				Referenced *int64 `json:"referenced_file_bytes"`
			} `json:"total"`
		}
		require.NoError(t, json.Unmarshal(out.Bytes(), &report), "bounded usage must emit its typed report: %s", out.String())
		require.Equal(t, 1, report.SchemaVersion)
		if bound == "1000" {
			require.NoError(t, err)
			require.False(t, report.Partial)
			require.NotNil(t, report.Total.Unique)
			require.Equal(t, int64(17<<20), *report.Total.Unique, "outside symlink must not contribute file payload")
			require.Equal(t, int64(1<<20), *report.Total.Referenced-*report.Total.Unique, "cross-class hardlink counted once in total")
			require.Less(t, *report.Total.Allocated, *report.Total.Unique, "sparse apparent bytes must not become allocated bytes")
		} else {
			require.Error(t, err)
			require.True(t, report.Partial)
			require.Nil(t, report.Total.Allocated)
			require.Nil(t, report.Total.Unique)
		}
	}
}
