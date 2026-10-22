//go:build linux

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nektos/act/pkg/artifactcache"
	"github.com/stretchr/testify/require"
)

func TestArchiveEnrollmentCommandKeepsFenceAndStoppedSourcePublicationSeparate(t *testing.T) {
	parent := t.TempDir()
	source, cohort := filepath.Join(parent, "repo"), filepath.Join(parent, "cohort")
	base := []string{"cache", "archive-enroll", "--from", source, "--namespace", "repo", "--cache-server-path", cohort, "--max-bytes", "160"}
	execute := func(flags ...string) (artifactcache.ArchiveEnrollmentReport, error) {
		command := createRootCommand(context.Background(), &Input{}, "test")
		var output bytes.Buffer
		command.SetOut(&output)
		command.SetArgs(append(append([]string{}, base...), flags...))
		err := command.Execute()
		var report artifactcache.ArchiveEnrollmentReport
		if output.Len() > 0 {
			require.NoError(t, json.Unmarshal(output.Bytes(), &report))
		}
		return report, err
	}
	for _, flags := range [][]string{nil, {"--apply"}, {"--source-quiescent"}, {"--apply", "--require-ready"}, {"--apply", "--fence-only", "--require-ready"}} {
		_, err := execute(flags...)
		require.Error(t, err)
		entries, err := os.ReadDir(parent)
		require.NoError(t, err)
		require.Empty(t, entries, "invalid authorization must not mutate storage")
	}
	// Seed actual HTTP reserve/upload/commit state, not fabricated Bolt rows.
	client := &http.Client{Timeout: 2 * time.Second}
	legacy, err := artifactcache.StartHandler(source, "", "127.0.0.1", 0, nil)
	require.NoError(t, err)
	contents := bytes.Repeat([]byte("warm archive"), 8)
	uploadCutoverArchive(t, client, legacy.ExternalURL(), contents)
	require.NoError(t, legacy.Close())
	fence, err := execute("--apply", "--fence-only")
	require.NoError(t, err)
	require.True(t, fence.SourceFenced)
	require.False(t, fence.Ready)
	_, err = execute("--require-ready")
	require.Error(t, err, "a fence alone cannot authorize routing")
	completed, err := execute("--apply", "--source-quiescent")
	require.NoError(t, err)
	require.True(t, completed.Ready)
	require.Equal(t, fence.Snapshot, completed.Snapshot)
	for range 2 {
		ready, err := execute("--require-ready")
		require.NoError(t, err)
		require.True(t, ready.Ready)
		policy := artifactcache.DefaultPolicy()
		policy.CohortRoot = cohort
		server, err := artifactcache.StartHandlerWithPolicy(filepath.Join(cohort, "repo"), "", "127.0.0.1", 0, nil, policy)
		require.NoError(t, err)
		assertCutoverArchive(t, client, server.ExternalURL(), contents)
		require.NoError(t, server.Close())
	}
	_, err = artifactcache.StartHandler(source, "", "127.0.0.1", 0, nil)
	require.Error(t, err)
}
