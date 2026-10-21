//go:build linux

package artifactcache

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestToolRecoveryCompletionInventoryHasFiniteOverflowAndExpiry(t *testing.T) {
	directory := t.TempDir()
	now := time.Now().UTC()
	var expiredPaths []string
	for index := 0; index < toolRecoveryPinLimit; index++ {
		pin := ToolRecoveryPin{1, fmt.Sprintf("%064x", index), strings.Repeat("a", 64), now, now.Add(time.Hour)}
		completion := ToolRecoveryCompletion{Pin: pin, Generation: strings.Repeat("b", 64), CompletedAt: now}
		data, err := json.Marshal(completion)
		require.NoError(t, err)
		path := filepath.Join(directory, pin.Owner+".json")
		require.NoError(t, os.WriteFile(path, data, 0600))
		if index < 32 {
			expiredPaths = append(expiredPaths, path)
		}
	}
	require.ErrorContains(t, boundToolRecoveryCompletions(context.Background(), directory), "full")
	for _, path := range expiredPaths {
		pin := ToolRecoveryPin{1, filepath.Base(path)[:64], strings.Repeat("a", 64), now.Add(-2 * time.Hour), now.Add(-time.Hour)}
		data, err := json.Marshal(ToolRecoveryCompletion{Pin: pin, Generation: strings.Repeat("b", 64), CompletedAt: now.Add(-90 * time.Minute)})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, data, 0600))
	}
	require.NoError(t, boundToolRecoveryCompletions(context.Background(), directory))
	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	require.Len(t, entries, toolRecoveryPinLimit-32, "a pass expires only its bounded batch")
	for _, path := range expiredPaths {
		require.NoFileExists(t, path)
	}
}

func TestToolRecoveryCompletionRefusesAmbiguousMetadata(t *testing.T) {
	for _, scenario := range []string{"oversized", "symlink", "unknown-field", "trailing", "timestamp", "owner-filename"} {
		t.Run(scenario, func(t *testing.T) {
			directory := t.TempDir()
			now := time.Now().UTC()
			pin := ToolRecoveryPin{1, strings.Repeat("a", 64), strings.Repeat("b", 64), now, now.Add(time.Hour)}
			completion := ToolRecoveryCompletion{Pin: pin, Generation: strings.Repeat("c", 64), CompletedAt: now}
			if scenario == "timestamp" {
				completion.CompletedAt = pin.ExpiresAt
			}
			data, err := json.Marshal(completion)
			require.NoError(t, err)
			switch scenario {
			case "oversized":
				data = []byte(strings.Repeat("x", toolRecoveryCompletionBytes+1))
			case "unknown-field":
				data = append(data[:len(data)-1], []byte(`,"extra":true}`)...)
			case "trailing":
				data = append(data, []byte(" {}")...)
			}
			path := filepath.Join(directory, pin.Owner+".json")
			if scenario == "owner-filename" {
				path = filepath.Join(directory, strings.Repeat("d", 64)+".json")
			}
			require.NoError(t, os.WriteFile(path, data, 0600))
			if scenario == "symlink" {
				require.NoError(t, os.Rename(path, path+"-original"))
				require.NoError(t, os.Symlink(path+"-original", path))
			}
			require.Error(t, boundToolRecoveryCompletions(context.Background(), directory))
		})
	}
}
