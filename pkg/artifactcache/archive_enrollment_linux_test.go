//go:build linux

package artifactcache

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func archiveEnrollmentFixture(t *testing.T, n int) (string, string) {
	t.Helper()
	parent := t.TempDir()
	source := filepath.Join(parent, "repo")
	require.NoError(t, os.Mkdir(source, 0700))
	storage, err := NewStorage(filepath.Join(source, "cache"))
	require.NoError(t, err)
	seedAuditEntries(t, &Handler{dir: source, storage: storage}, n)
	return source, filepath.Join(parent, "cohort")
}
func enrollArchiveTest(t *testing.T, source, root string, phase ArchiveEnrollmentPhase) ArchiveEnrollmentReport {
	t.Helper()
	r := EnrollArchive(context.Background(), source, root, "repo", 160, phase)
	require.False(t, r.Partial, r.Error)
	require.True(t, r.SourceFenced)
	return r
}
func TestArchiveEnrollmentPreservesWarmDataAndBlocksLegacyReopen(t *testing.T) {
	source, root := archiveEnrollmentFixture(t, 2)
	before, err := os.ReadFile(filepath.Join(source, "bolt.db"))
	require.NoError(t, err)
	fence := enrollArchiveTest(t, source, root, ArchiveEnrollmentFence)
	require.False(t, fence.Ready)
	_, err = StartHandler(source, "", "127.0.0.1", 0, nil)
	require.Error(t, err, "a pre-enrollment binary must not recreate the legacy namespace")
	_, err = os.Stat(filepath.Join(root, "repo"))
	require.True(t, os.IsNotExist(err), "fencing must not publish before writer drain")
	ready := enrollArchiveTest(t, source, root, ArchiveEnrollmentComplete)
	require.True(t, ready.Ready)
	after, err := os.ReadFile(filepath.Join(fence.Snapshot, "bolt.db"))
	require.NoError(t, err)
	require.Equal(t, before, after)
	policy := DefaultPolicy()
	policy.CohortRoot = root
	h, err := StartHandlerWithPolicy(filepath.Join(root, "repo"), "", "127.0.0.1", 0, nil, policy)
	require.NoError(t, err)
	defer h.Close()
	assertArchiveHit(t, h, "warm-1")
	require.True(t, enrollArchiveTest(t, source, root, ArchiveEnrollmentRequire).Ready)
}
func TestArchiveEnrollmentColdSourceStillNeedsPersistentHistory(t *testing.T) {
	for _, present := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "empty"}[present], func(t *testing.T) {
			parent := t.TempDir()
			source, root := filepath.Join(parent, "repo"), filepath.Join(parent, "cohort")
			if present {
				require.NoError(t, os.Mkdir(source, 0700))
			}
			fence := enrollArchiveTest(t, source, root, ArchiveEnrollmentFence)
			require.False(t, fence.Ready)
			require.True(t, enrollArchiveTest(t, source, root, ArchiveEnrollmentComplete).Ready)
			require.True(t, enrollArchiveTest(t, source, root, ArchiveEnrollmentRequire).Ready)
			require.NoError(t, os.Rename(archiveHistoryPath(source), archiveHistoryPath(source)+".held"))
			report := EnrollArchive(context.Background(), source, root, "repo", 160, ArchiveEnrollmentRequire)
			require.True(t, report.Partial)
			require.False(t, report.Ready)
		})
	}
}
func TestArchiveEnrollmentDoesNotMistakeFenceForStoppedDirectoryWriters(t *testing.T) {
	source, root := archiveEnrollmentFixture(t, 1)
	directory, err := os.Open(source)
	require.NoError(t, err)
	defer directory.Close()
	fence := enrollArchiveTest(t, source, root, ArchiveEnrollmentFence)
	oldPath := (&Storage{rootDir: filepath.Join("/proc/self/fd", strconv.FormatUint(uint64(directory.Fd()), 10), "cache")}).filename(1)
	// Existing directory FDs still reach the old tree after exchange. Merely
	// fencing the pathname cannot authorize publication or claim writer absence.
	original, err := os.ReadFile(oldPath)
	require.NoError(t, err)
	require.Equal(t, make([]byte, 80), original)
	require.NoError(t, os.WriteFile(oldPath, []byte("not the completed archive length"), 0600))
	current := AuditStore(context.Background(), fence.Snapshot, 0)
	require.True(t, current.Partial)
	refused := EnrollArchive(context.Background(), source, root, "repo", 160, ArchiveEnrollmentComplete)
	require.True(t, refused.Partial)
	require.False(t, refused.Ready)
	require.NoError(t, os.WriteFile(oldPath, make([]byte, 80), 0600))
	require.True(t, enrollArchiveTest(t, source, root, ArchiveEnrollmentComplete).Ready)
}
func TestArchiveEnrollmentRecoversPublishedImportAndRefusesMissingWarmState(t *testing.T) {
	source, root := archiveEnrollmentFixture(t, 1)
	fence := enrollArchiveTest(t, source, root, ArchiveEnrollmentFence)
	imported := ImportCompleted(context.Background(), fence.Snapshot, root, "repo", 160)
	require.False(t, imported.Partial, imported.Error)
	require.True(t, enrollArchiveTest(t, source, root, ArchiveEnrollmentComplete).Ready, "lost publication acknowledgement must reconcile")
	require.NoError(t, os.Rename(filepath.Join(root, "repo"), filepath.Join(root, "held")))
	for _, phase := range []ArchiveEnrollmentPhase{ArchiveEnrollmentFence, ArchiveEnrollmentComplete, ArchiveEnrollmentRequire} {
		r := EnrollArchive(context.Background(), source, root, "repo", 160, phase)
		require.True(t, r.Partial)
		require.False(t, r.Ready)
	}
	require.NoError(t, os.Rename(filepath.Join(root, "held"), filepath.Join(root, "repo")))
	require.True(t, enrollArchiveTest(t, source, root, ArchiveEnrollmentRequire).Ready)
	require.NoError(t, os.Rename(source, source+".held"))
	r := EnrollArchive(context.Background(), source, root, "repo", 160, ArchiveEnrollmentRequire)
	require.True(t, r.Partial)
	require.False(t, r.Ready)
}
func TestArchiveEnrollmentRejectsPolicyChangesAndUnownedDestinations(t *testing.T) {
	source, root := archiveEnrollmentFixture(t, 1)
	enrollArchiveTest(t, source, root, ArchiveEnrollmentFence)
	r := EnrollArchive(context.Background(), source, root, "repo", 161, ArchiveEnrollmentComplete)
	require.True(t, r.Partial)
	require.False(t, r.Ready)
	require.NoError(t, os.Mkdir(root, 0700))
	require.NoError(t, os.Mkdir(filepath.Join(root, "repo"), 0700))
	r = EnrollArchive(context.Background(), source, root, "repo", 160, ArchiveEnrollmentComplete)
	require.True(t, r.Partial)
	require.False(t, r.Ready, "directory existence is not publication evidence")
}

func TestArchiveEnrollmentRefusesRecreatedWarmMetadata(t *testing.T) {
	source, root := archiveEnrollmentFixture(t, 1)
	enrollArchiveTest(t, source, root, ArchiveEnrollmentFence)
	report := ArchiveEnrollmentReport{Source: source, Root: root, Namespace: "repo", MaxBytes: 160}
	db, h, err := loadArchiveHistory(context.Background(), &report, false)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	// Simulate a lost publication reply before testing recreated metadata.
	imported := ImportCompleted(context.Background(), h.snapshot(), root, "repo", 160)
	require.False(t, imported.Partial, imported.Error)
	require.True(t, enrollArchiveTest(t, source, root, ArchiveEnrollmentComplete).Ready)
	metadata := filepath.Join(root, "repo", "bolt.db")
	require.NoError(t, os.Rename(metadata, metadata+".held"))
	empty, err := (&Handler{dir: filepath.Join(root, "repo")}).openDB()
	require.NoError(t, err)
	require.NoError(t, empty.Close())
	refused := EnrollArchive(context.Background(), source, root, "repo", 160, ArchiveEnrollmentRequire)
	require.True(t, refused.Partial)
	require.Contains(t, refused.Error, "incarnation")
}

func TestArchiveEnrollmentDoesNotAdoptPreexistingUntypedJournal(t *testing.T) {
	source, root := archiveEnrollmentFixture(t, 1)
	db, _, err := openEnrollmentHistoryFile(context.Background(), archiveHistoryPath(source), true)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	before, err := os.ReadFile(archiveHistoryPath(source))
	require.NoError(t, err)
	r := EnrollArchive(context.Background(), source, root, "repo", 160, ArchiveEnrollmentFence)
	require.True(t, r.Partial)
	require.False(t, r.SourceFenced)
	after, err := os.ReadFile(archiveHistoryPath(source))
	require.NoError(t, err)
	require.Equal(t, before, after)
	info, err := os.Stat(source)
	require.NoError(t, err)
	require.True(t, info.IsDir())
}

func TestArchiveEnrollmentUsesDurableIncarnationAcrossFileReopen(t *testing.T) {
	source, root := archiveEnrollmentFixture(t, 1)
	enrollArchiveTest(t, source, root, ArchiveEnrollmentFence)
	enrollArchiveTest(t, source, root, ArchiveEnrollmentComplete)
	metadata := filepath.Join(root, "repo", "bolt.db")
	before, err := archivePathIdentity(metadata, false)
	require.NoError(t, err)
	input, err := os.Open(metadata)
	require.NoError(t, err)
	output, err := os.OpenFile(metadata+".copy", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	require.NoError(t, err)
	_, err = io.Copy(output, input)
	require.NoError(t, err)
	require.NoError(t, output.Close())
	require.NoError(t, input.Close())
	require.NoError(t, os.Rename(metadata+".copy", metadata))
	after, err := archivePathIdentity(metadata, false)
	require.NoError(t, err)
	require.NotEqual(t, before, after)
	require.True(t, enrollArchiveTest(t, source, root, ArchiveEnrollmentRequire).Ready,
		"retained durable incarnation must not depend on a previous mount/inode number")
}

func TestArchiveEnrollmentLostImportReplyCannotBlessReplacedWarmInventory(t *testing.T) {
	source, root := archiveEnrollmentFixture(t, 1)
	fence := enrollArchiveTest(t, source, root, ArchiveEnrollmentFence)
	imported := ImportCompleted(context.Background(), fence.Snapshot, root, "repo", 160)
	require.False(t, imported.Partial, imported.Error)
	destination := filepath.Join(root, "repo")
	require.NoError(t, os.Rename(filepath.Join(destination, "bolt.db"), filepath.Join(destination, "bolt.db.held")))
	require.NoError(t, os.Rename(filepath.Join(destination, "cache"), filepath.Join(destination, "cache.held")))
	_, err := NewStorage(filepath.Join(destination, "cache"))
	require.NoError(t, err)
	empty, err := (&Handler{dir: destination}).openDB()
	require.NoError(t, err)
	require.NoError(t, empty.Close())
	audit := AuditStore(context.Background(), destination, 0)
	require.False(t, audit.Partial)
	require.EqualValues(t, 0, *audit.EntryCount)
	report := EnrollArchive(context.Background(), source, root, "repo", 160, ArchiveEnrollmentComplete)
	require.True(t, report.Partial, "a valid empty store cannot replace the unacknowledged warm publication")
	require.False(t, report.Ready)
}

func TestArchiveEnrollmentResumesIntentBeforePrivateStageCreation(t *testing.T) {
	source, root := archiveEnrollmentFixture(t, 1)
	enrollArchiveTest(t, source, root, ArchiveEnrollmentFence)
	report := ArchiveEnrollmentReport{Source: source, Root: root, Namespace: "repo", MaxBytes: 160}
	db, h, err := loadArchiveHistory(context.Background(), &report, false)
	require.NoError(t, err)
	h.ImportStarted = true
	require.NoError(t, storeArchiveHistory(db, h))
	require.NoError(t, db.Close())
	recovered := EnrollArchive(context.Background(), source, root, "repo", 160, ArchiveEnrollmentComplete)
	require.False(t, recovered.Partial, recovered.Error)
	require.True(t, recovered.Ready, "durable intent before stage creation must recover without manual repair")
}
