package fastcopy

import (
	"flag"
	"fmt"
	"io"
	"os"
)

var enableFastcopyReflinking = flag.Bool("executor.enable_fastcopy_reflinking", false, "If true, attempt to use `cp --reflink=auto` to link files")

// copyFile creates an independent inode without overwriting an existing path.
// io.Copy uses the kernel's file-to-file copy support where available.
func copyFile(source, destination string) (err error) {
	src, err := os.Open(source)
	if err != nil {
		return err
	}
	defer src.Close()
	info, err := src.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("cannot copy non-regular file %q", source)
	}
	dst, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := dst.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			os.Remove(destination)
		}
	}()
	if _, err := io.Copy(dst, src); err != nil {
		return err
	}
	return dst.Chmod(info.Mode().Perm())
}
