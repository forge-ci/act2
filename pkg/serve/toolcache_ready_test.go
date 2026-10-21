//go:build !windows

package serve

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNativePreparedBindingSurvivesRestartAndRejectsChanges(t *testing.T) {
	s, _, _ := saveServer(t)
	s.cfg.Socket = filepath.Join(t.TempDir(), "serve.sock")
	generation := ToolGeneration{ID: strings.Repeat("a", 64), Store: "/store"}
	generation.OverlayRecipeSHA256 = strings.TrimPrefix(digestOf([]byte(OverlayRecipe(generation.Store, s.cfg.ToolCacheMount))), "sha256:")
	encoded, err := json.Marshal(toolcacheReady{SchemaVersion: 1, Mode: "generation", Generation: &generation})
	require.NoError(t, err)
	write(t, s.readyStamp(), string(encoded))
	// A new server has no process-local knowledge of the earlier preparation.
	restarted := &Server{cfg: s.cfg}
	mode, err := restarted.prepareToolcache(context.Background(), &generation)
	require.NoError(t, err)
	require.Equal(t, "generation "+generation.ID, mode)
	prefixed := generation
	prefixed.OverlayRecipeSHA256 = "sha256:" + prefixed.OverlayRecipeSHA256
	_, err = restarted.prepareToolcache(context.Background(), &prefixed)
	require.NoError(t, err, "equivalent digest spelling is canonicalized at the boundary")
	for _, change := range []string{"id", "store", "recipe", "target"} {
		changed := generation
		candidate := &Server{cfg: s.cfg}
		switch change {
		case "id":
			changed.ID = strings.Repeat("b", 64)
		case "store":
			changed.Store = "/other-store"
			changed.OverlayRecipeSHA256 = digestOf([]byte(OverlayRecipe(changed.Store, s.cfg.ToolCacheMount)))
		case "recipe":
			changed.OverlayRecipeSHA256 = strings.Repeat("f", 64)
		case "target":
			candidate.cfg.ToolCacheMount = "/different-mount"
		}
		_, err := candidate.prepareToolcache(context.Background(), &changed)
		require.Error(t, err, change)
		after, err := os.ReadFile(s.readyStamp())
		require.NoError(t, err)
		require.Equal(t, encoded, after, "refusal preserves the prior binding")
	}
}

func TestMalformedPreparationNeverFallsBackToSeeding(t *testing.T) {
	for _, scenario := range []string{"unknown-field", "trailing", "oversized", "symlink", "directory", "unknown-schema"} {
		t.Run(scenario, func(t *testing.T) {
			s, _, _ := saveServer(t)
			s.cfg.Socket = filepath.Join(t.TempDir(), "serve.sock")
			data := `{"schema_version":1,"mode":"seeded"}`
			switch scenario {
			case "unknown-field":
				data = `{"schema_version":1,"mode":"seeded","extra":true}`
			case "trailing":
				data += " {}"
			case "oversized":
				data = strings.Repeat("x", toolcacheReadyLimit+1)
			case "unknown-schema":
				data = `{"schema_version":2,"mode":"seeded"}`
			}
			if scenario == "symlink" {
				write(t, s.readyStamp()+".other", data)
				require.NoError(t, os.Symlink(s.readyStamp()+".other", s.readyStamp()))
			} else if scenario == "directory" {
				require.NoError(t, os.Mkdir(s.readyStamp(), 0o700))
			} else {
				write(t, s.readyStamp(), data)
			}
			_, err := s.prepareToolcache(context.Background(), nil)
			require.Error(t, err)
		})
	}
}

func TestHistoricalSeededPreparationStillReusesItsCache(t *testing.T) {
	s, _, _ := saveServer(t)
	s.cfg.Socket = filepath.Join(t.TempDir(), "serve.sock")
	write(t, s.readyStamp(), "seeded")
	mode, err := s.prepareToolcache(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, "seeded", mode)
}

func TestHealthKeepsToolCacheStatusStrings(t *testing.T) {
	for _, mode := range []string{"historical-seed", "seeded", "generation", "unknown-schema", "oversized", "unverifiable-native"} {
		t.Run(mode, func(t *testing.T) {
			s, _, _ := saveServer(t)
			s.cfg.Socket = filepath.Join(t.TempDir(), "serve.sock")
			state := toolcacheReady{SchemaVersion: 1, Mode: "seeded"}
			expected := "seeded"
			if mode == "generation" {
				state.Mode = "generation"
				state.Generation = &ToolGeneration{ID: strings.Repeat("a", 64), Store: "/store"}
				state.Generation.OverlayRecipeSHA256 = strings.TrimPrefix(digestOf([]byte(OverlayRecipe("/store", s.cfg.ToolCacheMount))), "sha256:")
				expected = "generation " + state.Generation.ID
			}
			if mode == "unknown-schema" {
				state.SchemaVersion, expected = 2, ""
			}
			data, err := json.Marshal(state)
			require.NoError(t, err)
			switch mode {
			case "historical-seed":
				data = []byte("seeded")
			case "oversized":
				data, expected = []byte(strings.Repeat("x", toolcacheReadyLimit+1)), ""
			case "unverifiable-native":
				data, expected = []byte("generation "+strings.Repeat("a", 64)), ""
			}
			write(t, s.readyStamp(), string(data))
			recorder := httptest.NewRecorder()
			s.health(recorder, nil)
			var health Health
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &health))
			require.Equal(t, expected, health.ToolCache)
		})
	}
}
