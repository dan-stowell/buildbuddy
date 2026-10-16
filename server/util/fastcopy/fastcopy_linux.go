//go:build linux && !android

package fastcopy

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func Clone(source, destination string) error {
	if *enableFastcopyReflinking {
		return reflink(source, destination)
	}
	return FastCopy(source, destination)
}

func FastCopy(source, destination string) error {
	err := os.Link(source, destination)
	if !os.IsExist(err) {
		return err
	}
	return nil
}

func reflink(source, destination string) error {
	sourceFile, err := os.Open(source)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	info, err := sourceFile.Stat()
	if err != nil {
		return err
	}
	destFile, err := os.OpenFile(destination, os.O_RDWR|os.O_CREATE|os.O_EXCL, info.Mode())
	if err != nil {
		return err
	}
	defer destFile.Close()

	reflinkWasSuccessful := false
	defer func() {
		if !reflinkWasSuccessful {
			os.Remove(destination)
		}
	}()

	sourceConn, err := sourceFile.SyscallConn()
	if err != nil {
		return err
	}
	destConn, err := destFile.SyscallConn()
	if err != nil {
		return err
	}

	var destErr, sourceErr, ioctlErr error
	destErr = destConn.Control(func(dfd uintptr) {
		sourceErr = sourceConn.Control(func(sfd uintptr) {
			ioctlErr = unix.IoctlFileClone(int(dfd), int(sfd))
		})
	})
	if ioctlErr != nil {
		return ioctlErr
	}
	if sourceErr != nil {
		return sourceErr
	}
	if destErr != nil {
		return destErr
	}
	reflinkWasSuccessful = true
	return nil
}

// CloneOrCopy creates an independent inode, using a reflink when enabled and
// supported. Unlike Clone and FastCopy, it never falls back to a hardlink.
func CloneOrCopy(source, destination string) error {
	if *enableFastcopyReflinking {
		err := reflink(source, destination)
		if err == nil {
			return nil
		}
		if !errors.Is(err, unix.EOPNOTSUPP) && !errors.Is(err, unix.EXDEV) && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOSYS) && !errors.Is(err, unix.ENOTTY) {
			return err
		}
	}
	return copyFile(source, destination)
}
