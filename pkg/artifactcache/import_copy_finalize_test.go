package artifactcache

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVerifiedFileFinalizationFailureAndMutationRefuseSuccess(t *testing.T) {
	for _, scenario := range []string{"sync-error", "changed-source", "changed-destination"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			source, destination := filepath.Join(root, "source"), filepath.Join(root, "destination")
			require.NoError(t, os.WriteFile(source, []byte("warm"), 0600))
			var original *os.File
			digest, err := copyVerifiedFile(context.Background(), source, destination, 4, func(output *os.File) error {
				original = output
				// Finalization receives the original writable descriptor, before close.
				_, statErr := output.Stat()
				require.NoError(t, statErr)
				switch scenario {
				case "sync-error":
					return syscall.EIO
				case "changed-source":
					require.NoError(t, os.WriteFile(source, []byte("cold"), 0600))
				case "changed-destination":
					_, writeErr := output.WriteAt([]byte("cold"), 0)
					require.NoError(t, writeErr)
				}
				return output.Sync()
			})
			require.Error(t, err)
			require.Empty(t, digest)
			if scenario == "sync-error" {
				require.ErrorIs(t, err, syscall.EIO)
			}
			require.NotNil(t, original)
			_, err = original.Stat()
			require.ErrorIs(t, err, os.ErrClosed)
		})
	}
}
