package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/nektos/act/pkg/artifactcache"
	"github.com/stretchr/testify/require"
)

func TestToolEnrollmentCommandGuardsColdCreationAndRepeats(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("native tool enrollment requires Linux")
	}
	base := t.TempDir()
	source, store := filepath.Join(base, "legacy"), filepath.Join(base, "store")
	args := []string{"cache", "tool-enroll", "--from", source, "--cache-server-path", store, "--max-bytes", "100"}
	for _, flags := range [][]string{nil, {"--apply"}, {"--source-quiescent"}} {
		root := createRootCommand(context.Background(), &Input{}, "test")
		root.SetArgs(append(append([]string{}, args...), flags...))
		require.Error(t, root.Execute())
		entries, err := os.ReadDir(base)
		require.NoError(t, err)
		require.Empty(t, entries, "missing writer exclusion must not create history or native store")
	}
	var generation string
	for range 2 {
		root := createRootCommand(context.Background(), &Input{}, "test")
		var output bytes.Buffer
		root.SetOut(&output)
		root.SetArgs(append(append([]string{}, args...), "--source-quiescent", "--apply"))
		require.NoError(t, root.Execute())
		var report artifactcache.ToolEnrollmentReport
		require.NoError(t, json.Unmarshal(output.Bytes(), &report))
		require.False(t, report.Partial, report.Error)
		require.True(t, report.Enrolled)
		require.Equal(t, "cold", report.Mode)
		require.Len(t, report.Generation, 64)
		if generation != "" {
			require.Equal(t, generation, report.Generation)
		}
		generation = report.Generation
	}
}

func TestToolEnrollmentCommandKeepsFenceAndPublicationSeparate(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("native tool enrollment requires Linux")
	}
	base := t.TempDir()
	source, store := filepath.Join(base, "legacy"), filepath.Join(base, "store")
	args := []string{"cache", "tool-enroll", "--from", source, "--cache-server-path", store, "--max-bytes", "100", "--apply"}
	execute := func(flags ...string) (artifactcache.ToolEnrollmentReport, error) {
		root := createRootCommand(context.Background(), &Input{}, "test")
		var output bytes.Buffer
		root.SetOut(&output)
		root.SetArgs(append(append([]string{}, args...), flags...))
		err := root.Execute()
		var report artifactcache.ToolEnrollmentReport
		if output.Len() != 0 {
			require.NoError(t, json.Unmarshal(output.Bytes(), &report))
		}
		return report, err
	}
	_, err := execute("--fence-only", "--require-fenced")
	require.Error(t, err)
	entries, err := os.ReadDir(base)
	require.NoError(t, err)
	require.Empty(t, entries, "contradictory phases must refuse before mutation")
	fence, err := execute("--fence-only")
	require.NoError(t, err)
	require.True(t, fence.SourceFenced)
	require.False(t, fence.Enrolled)
	require.Empty(t, fence.Generation)
	_, err = artifactcache.CurrentToolGeneration(context.Background(), store, 100)
	require.Error(t, err, "fence phase accidentally published a generation")
	done, err := execute("--require-fenced", "--source-quiescent")
	require.NoError(t, err)
	require.True(t, done.SourceFenced && done.Enrolled)
	require.Len(t, done.Generation, 64)
}

func TestToolOperationCommandsRejectInvalidTimeoutBeforeMutation(t *testing.T) {
	for _, command := range []string{"tool-enroll", "tool-recovery"} {
		for _, value := range []string{"0", "-1", "43201"} {
			store := t.TempDir()
			root := createRootCommand(context.Background(), &Input{}, "test")
			args := []string{"cache", command}
			if command == "tool-recovery" {
				args = append(args, "reserve")
			}
			args = append(args, "--cache-server-path", store, "--apply", "--timeout-secs", value)
			if command == "tool-enroll" {
				args = append(args, "--fence-only")
			}
			root.SetArgs(args)
			require.ErrorContains(t, root.Execute(), "tool operation timeout must")
			entries, err := os.ReadDir(store)
			require.NoError(t, err)
			require.Empty(t, entries)
		}
	}
}

func TestToolEnrollmentAcceptsExplicitOperationTimeout(t *testing.T) {
	base := t.TempDir()
	root := createRootCommand(context.Background(), &Input{}, "test")
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetArgs([]string{"cache", "tool-enroll", "--from", filepath.Join(base, "legacy"), "--cache-server-path", filepath.Join(base, "store"), "--max-bytes", "100", "--apply", "--fence-only", "--timeout-secs", "30"})
	require.NoError(t, root.Execute())
	var report artifactcache.ToolEnrollmentReport
	require.NoError(t, json.Unmarshal(output.Bytes(), &report))
	require.True(t, report.SourceFenced)
}
