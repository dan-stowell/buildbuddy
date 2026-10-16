package hostid

import (
	"os"
	"path/filepath"
	"runtime/debug"
	"testing"

	"github.com/buildbuddy-io/buildbuddy/server/testutil/testleak"
	"github.com/stretchr/testify/require"
)

func TestGetOrCreateHostIDClosesFile(t *testing.T) {
	// An unclosed *os.File is eventually closed by its finalizer. Disable GC
	// so that the check doesn't depend on whether a GC happens to run.
	defer debug.SetGCPercent(debug.SetGCPercent(-1))

	for _, tc := range []struct {
		name     string
		existing bool
	}{
		{"create", false},
		{"read_existing", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.existing {
				err := os.WriteFile(filepath.Join(dir, hostIDFilename), []byte("test-host-id"), 0644)
				require.NoError(t, err)
			}
			testleak.CheckFDs(t)

			id, err := getOrCreateHostId(dir)
			require.NoError(t, err)
			require.NotEmpty(t, id)
			if tc.existing {
				require.Equal(t, "test-host-id", id)
			}
		})
	}
}
