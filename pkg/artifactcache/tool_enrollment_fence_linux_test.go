//go:build linux

package artifactcache

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestToolEnrollmentPhysicallyFencesOlderLegacyWriters(t *testing.T) {
	for _, mode := range []string{"cold", "warm"} {
		t.Run(mode, func(t *testing.T) {
			source, root := toolBootstrapFixture(t)
			if mode == "cold" {
				require.NoError(t, os.Rename(source, source+".unused"))
			}
			report := EnrollToolGeneration(context.Background(), source, root, 100)
			require.False(t, report.Partial, report.Error)
			info, err := os.Lstat(source)
			require.NoError(t, err)
			require.True(t, info.Mode().IsRegular(), "original legacy path must be a durable refusal file")
			require.Error(t, os.MkdirAll(source, 0755), "older seed/save mkdir must fail before opening cache payload")
		})
	}
}

func TestToolEnrollmentFenceRefusesActualLegacySeedAndSaveRecipes(t *testing.T) {
	source, root := toolBootstrapFixture(t)
	report := EnrollToolGeneration(context.Background(), source, root, 100)
	require.False(t, report.Partial, report.Error)
	before, err := os.ReadFile(source)
	require.NoError(t, err)
	target := t.TempDir()
	bin := t.TempDir()
	called := filepath.Join(bin, "docker-called")
	docker := filepath.Join(bin, "docker")
	require.NoError(t, os.WriteFile(docker, []byte("#!/bin/sh\nprintf called >\"$BOSN_ENROLL_DOCKER_CALLED\"\nexit 99\n"), 0600))
	require.NoError(t, os.Chmod(docker, 0700))
	for _, recipe := range []struct {
		name    string
		command *exec.Cmd
	}{
		{"seed", exec.Command("sh", "../serve/scripts/toolcache_seed.sh")},
		{"save", exec.Command("sh", "../serve/scripts/toolcache_save.sh")},
	} {
		t.Run(recipe.name, func(t *testing.T) {
			command := recipe.command
			command.Env = append(os.Environ(), "ACT_TOOLCACHE_STORE="+source,
				"ACT_TOOLCACHE_MOUNT="+target, "ACT_TOOLCACHE_VOLUME=enrollment-refusal-fixture",
				"ACT_STALE_STAGE_MINUTES=60", "BOSN_ENROLL_DOCKER_CALLED="+called,
				"PATH="+bin+":"+os.Getenv("PATH"))
			output, err := command.CombinedOutput()
			require.Error(t, err)
			require.Contains(t, string(output), "mkdir", "legacy recipe must fail at original store-path admission")
			_, err = os.Lstat(called)
			require.True(t, os.IsNotExist(err), "seed recipe created a cold tool volume after refusal")
			after, err := os.ReadFile(source)
			require.NoError(t, err)
			require.Equal(t, before, after, "legacy recipe changed enrollment refusal authority")
		})
	}
}

func TestToolEnrollmentFencePrecedesOldDirectoryWriterDrainAndPublication(t *testing.T) {
	source, root := toolBootstrapFixture(t)
	oldDirectory, err := os.Open(source)
	require.NoError(t, err)
	fenced := FenceToolEnrollmentSource(context.Background(), source, root, 100)
	require.False(t, fenced.Partial, fenced.Error)
	require.True(t, fenced.SourceFenced)
	require.False(t, fenced.Enrolled)
	require.Empty(t, fenced.Generation)
	_, err = os.Lstat(filepath.Join(root, toolGenerationCurrent))
	require.True(t, os.IsNotExist(err), "fencing must not scan or publish while old directory-FD writers may remain")
	// Simulate a pre-claim peer that opened this directory before cutover.
	// Its descriptor survives exchange; publication waits for this writer to
	// finish and close, rather than pretending the source fence revoked it.
	late := fmt.Sprintf("/proc/self/fd/%d/Go/2/x64", oldDirectory.Fd())
	require.NoError(t, os.MkdirAll(late, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(late, "tool"), []byte("late"), 0600))
	require.NoError(t, os.WriteFile(late+".complete", nil, 0600))
	require.NoError(t, oldDirectory.Close())
	done := CompleteToolEnrollment(context.Background(), source, root, 100)
	require.False(t, done.Partial, done.Error)
	require.True(t, done.Enrolled && done.SourceFenced)
	state, err := CurrentToolGenerationState(context.Background(), root, 100)
	require.NoError(t, err)
	require.Len(t, state.Installs, 3, "the drained writer's completed install must survive cutover")
	payload, err := os.ReadFile(filepath.Join(root, toolGenerationDirectory, done.Generation, "tree", "Go/2/x64/tool"))
	require.NoError(t, err)
	require.Equal(t, "late", string(payload))
}

func TestToolEnrollmentInitiallyEmptySourcePreservesDrainedWriterInstall(t *testing.T) {
	base := t.TempDir()
	source, root := filepath.Join(base, "legacy"), filepath.Join(base, "store")
	require.NoError(t, os.Mkdir(source, 0755))
	oldDirectory, err := os.Open(source)
	require.NoError(t, err)
	fenced := FenceToolEnrollmentSource(context.Background(), source, root, 100)
	require.False(t, fenced.Partial, fenced.Error)
	require.Equal(t, "cold", fenced.Mode)
	late := fmt.Sprintf("/proc/self/fd/%d/Go/2/x64", oldDirectory.Fd())
	require.NoError(t, os.MkdirAll(late, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(late, "tool"), []byte("late"), 0600))
	require.NoError(t, os.WriteFile(late+".complete", nil, 0600))
	require.NoError(t, oldDirectory.Close())
	done := CompleteToolEnrollment(context.Background(), source, root, 100)
	require.False(t, done.Partial, done.Error)
	require.Equal(t, "warm", done.Mode)
	payload, err := os.ReadFile(filepath.Join(root, toolGenerationDirectory, done.Generation, "tree", "Go/2/x64/tool"))
	require.NoError(t, err)
	require.Equal(t, "late", string(payload))
}

func TestToolEnrollmentReconcilesLegacyDirectoryCreatedBeforeFence(t *testing.T) {
	base := t.TempDir()
	source, root := filepath.Join(base, "legacy"), filepath.Join(base, "store")
	db, _, err := openToolEnrollmentHistory(context.Background(), root)
	require.NoError(t, err)
	history, err := beginToolEnrollmentHistory(db, source, root)
	require.NoError(t, err)
	require.False(t, history.SourcePresent)
	require.NoError(t, db.Close())
	install := filepath.Join(source, "Go/2/x64")
	require.NoError(t, os.MkdirAll(install, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(install, "tool"), []byte("late"), 0600))
	require.NoError(t, os.WriteFile(install+".complete", nil, 0600))
	fenced := FenceToolEnrollmentSource(context.Background(), source, root, 100)
	require.False(t, fenced.Partial, fenced.Error)
	require.Equal(t, "warm", fenced.Mode)
	done := CompleteToolEnrollment(context.Background(), source, root, 100)
	require.False(t, done.Partial, done.Error)
	payload, err := os.ReadFile(filepath.Join(root, toolGenerationDirectory, done.Generation, "tree", "Go/2/x64/tool"))
	require.NoError(t, err)
	require.Equal(t, "late", string(payload))
}

func TestToolEnrollmentCompleteCannotCreateOrRepairFence(t *testing.T) {
	source, root := toolBootstrapFixture(t)
	before, err := os.ReadDir(filepath.Dir(root))
	require.NoError(t, err)
	missing := CompleteToolEnrollment(context.Background(), source, root, 100)
	require.True(t, missing.Partial)
	after, err := os.ReadDir(filepath.Dir(root))
	require.NoError(t, err)
	require.Len(t, after, len(before), "completion without history must not mutate the cache")
	fence := FenceToolEnrollmentSource(context.Background(), source, root, 100)
	require.False(t, fence.Partial, fence.Error)
	require.NoError(t, os.Rename(source, source+".fence"))
	require.NoError(t, os.Mkdir(source, 0755))
	path := filepath.Join(source, "unexpected")
	require.NoError(t, os.WriteFile(path, []byte("preserved"), 0600))
	report := CompleteToolEnrollment(context.Background(), source, root, 100)
	require.True(t, report.Partial, "completion silently performed another source exchange")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "preserved", string(data))
	_, err = os.Lstat(filepath.Join(root, toolGenerationCurrent))
	require.True(t, os.IsNotExist(err))
}

func TestToolEnrollmentFenceReconcilesLostExchangeReply(t *testing.T) {
	source, root := toolBootstrapFixture(t)
	db, _, err := openToolEnrollmentHistory(context.Background(), root)
	require.NoError(t, err)
	history, err := beginToolEnrollmentHistory(db, source, root)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	calls := 0
	err = fenceToolEnrollmentSource(*history, func(from, to string, flags uint) error {
		calls++
		if err := unix.Renameat2(unix.AT_FDCWD, from, unix.AT_FDCWD, to, flags); err != nil {
			return err
		}
		return fmt.Errorf("injected lost exchange reply")
	}, syncToolDirectory)
	require.ErrorContains(t, err, "lost exchange reply")
	require.NoError(t, requireToolEnrollmentSourceFence(*history))
	require.NoError(t, requirePendingToolEnrollmentSnapshot(*history))
	require.NoError(t, fenceToolEnrollmentSource(*history, func(string, string, uint) error {
		calls++
		return fmt.Errorf("exchange must not be repeated")
	}, syncToolDirectory))
	require.Equal(t, 1, calls)
	retry := FenceToolEnrollmentSource(context.Background(), source, root, 100)
	require.False(t, retry.Partial, retry.Error)
	require.False(t, retry.Enrolled)
}
