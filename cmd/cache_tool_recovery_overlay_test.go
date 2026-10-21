package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nektos/act/pkg/artifactcache"
	"github.com/stretchr/testify/require"
)

func TestToolRecoveryOverlayRefusesUnknownProducerBeforeMount(t *testing.T) {
	now := time.Now().UTC()
	pin := artifactcache.ToolRecoveryPin{SchemaVersion: 1, Owner: strings.Repeat("a", 64), Generation: strings.Repeat("b", 64), CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	record := filepath.Join(t.TempDir(), "pin.json")
	data, err := json.Marshal(pin)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(record, data, 0600))
	root := createRootCommand(context.Background(), &Input{}, "test")
	root.SetArgs([]string{"cache", "tool-recovery", "recover", "--cache-server-path", t.TempDir(), "--record", record, "--source-root", t.TempDir(), "--toolcache-target", "/opt/hostedtoolcache", "--overlay-recipe-sha256", strings.Repeat("0", 64), "--max-bytes", "100", "--apply", "--source-quiescent"})
	require.ErrorContains(t, root.Execute(), "differs from frozen producer")
}
