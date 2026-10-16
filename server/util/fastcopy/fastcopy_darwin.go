//go:build darwin && !ios

package fastcopy

import (
	"errors"
	"flag"
	"os"

	"golang.org/x/sys/unix"
)

var localCacheUseHardlinks = flag.Bool("executor.local_cache_use_hardlinks", false, "If true, use hardlinks instead of copy-on-write clones")

func Clone(source, destination string) error {
	if err := unix.Clonefile(source, destination, unix.CLONE_NOFOLLOW); err != nil && !errors.Is(err, unix.EEXIST) {
		return err
	}
	return nil
}

func FastCopy(source, destination string) error {
	if *localCacheUseHardlinks {
		if err := unix.Linkat(unix.AT_FDCWD, source, unix.AT_FDCWD, destination, 0); err != nil && !errors.Is(err, unix.EEXIST) {
			return err
		}
		return nil
	}
	return Clone(source, destination)
}

// CloneOrCopy creates an independent inode, using the platform's native clone
// implementation when possible and copying otherwise.
func CloneOrCopy(source, destination string) error {
	if err := unix.Clonefile(source, destination, unix.CLONE_NOFOLLOW); err == nil {
		return nil
	} else if os.IsExist(err) {
		return err
	}
	return copyFile(source, destination)
}
