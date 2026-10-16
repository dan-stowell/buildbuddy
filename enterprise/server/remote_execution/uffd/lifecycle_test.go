package uffd

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/buildbuddy-io/buildbuddy/server/testutil/testleak"
	"github.com/stretchr/testify/require"
)

// The setup protocol only transfers a descriptor; a pipe stands in for UFFD so
// this test needs neither userfaultfd privileges nor a running Firecracker VM.
func TestReceiveSetupMessageFDGrowth(t *testing.T) {
	for _, n := range []int{10, 50} {
		t.Run(fmt.Sprintf("N=%d", n), func(t *testing.T) {
			root := t.TempDir()
			testleak.CheckGrowth(t, testleak.Growth{Iterations: n}, func(i int) {
				path := filepath.Join(root, "setup.sock")
				lis, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
				require.NoError(t, err)
				h := &Handler{lis: lis, stoppedChan: make(chan struct{})}
				r, w, err := os.Pipe()
				require.NoError(t, err)
				conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
				require.NoError(t, err)
				_, _, err = conn.WriteMsgUnix([]byte("[]"), syscall.UnixRights(int(r.Fd())), nil)
				require.NoError(t, err)
				msg, err := h.receiveSetupMsg(t.Context())
				require.NoError(t, err)
				require.NoError(t, syscall.Close(int(msg.Uffd)))
				require.NoError(t, conn.Close())
				require.NoError(t, r.Close())
				require.NoError(t, w.Close())
				// receiveSetupMsg closes its listener asynchronously; close it here too
				// so all handshake owners have finished before sampling.
				_ = lis.Close()
			})
		})
	}
}
