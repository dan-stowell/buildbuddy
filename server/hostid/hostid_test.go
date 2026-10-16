package hostid

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/buildbuddy-io/buildbuddy/server/testutil/testleak"
)

// The public GetHostID memoizes this helper, so production retains at most one
// descriptor, rather than one descriptor per action. Both file-opening paths
// must release their descriptor when the helper returns, without waiting for GC.
func TestHostIDClosesFile(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "create"
		if existing {
			name = "read_existing"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if existing {
				if err := os.WriteFile(filepath.Join(dir, "host_id"), []byte("test-host-id"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			testleak.CheckFDs(t)
			if _, err := getOrCreateHostId(dir); err != nil {
				t.Fatal(err)
			}
		})
	}
}
