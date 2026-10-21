//go:build linux && !WITHOUT_DOCKER

package container

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHostedRunnerOwnershipHandlesOnlyDisappearedCacheEntries(t *testing.T) {
	// Exercise the embedded ownership phase without creating host user accounts.
	start := strings.Index(hostedRunnerSetup, "# Restored completed installs")
	end := strings.Index(hostedRunnerSetup, "if command -v sudo")
	require.GreaterOrEqual(t, start, 0)
	require.Greater(t, end, start)
	script := "set -eu\n" + hostedRunnerSetup[start:end]
	for _, scenario := range []string{"disappeared", "retained", "dangling-link"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			store := filepath.Join(root, "store")
			require.NoError(t, os.Mkdir(store, 0o755))
			entry := filepath.Join(store, "racing-entry")
			if scenario == "dangling-link" {
				require.NoError(t, os.Symlink(filepath.Join(root, "missing-target"), entry))
			} else {
				require.NoError(t, os.WriteFile(entry, []byte("cache"), 0o600))
			}
			bin := filepath.Join(root, "bin")
			require.NoError(t, os.Mkdir(bin, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(bin, "id"), []byte("#!/bin/sh\necho 65534\n"), 0o755)) //nolint:gosec // isolated test executable
			// A concurrent installer withdraws its temporary file during chown. The
			// same error while the entry remains must continue to fail provisioning.
			shim := `#!/bin/sh
for entry do :; done
if [ "$entry" = "$STORE" ]; then
 case " $* " in *" -R "*) entry="$STORE/racing-entry" ;; *) exit 0 ;; esac
fi
if [ "$SCENARIO" = disappeared ]; then mv "$entry" "$WITHDRAWN"; fi
exit 1
`
			require.NoError(t, os.WriteFile(filepath.Join(bin, "chown"), []byte(shim), 0o755)) //nolint:gosec // isolated test executable
			cmd := exec.Command("sh", "-ec", script, "ownership-test", "", store, "")
			cmd.Env = append(os.Environ(), "PATH="+bin+":/usr/bin:/bin", "STORE="+store, "SCENARIO="+scenario, "WITHDRAWN="+filepath.Join(root, "withdrawn"))
			output, err := cmd.CombinedOutput()
			if scenario == "disappeared" {
				require.NoError(t, err, string(output))
			} else {
				require.Error(t, err, string(output))
			}
		})
	}
}
