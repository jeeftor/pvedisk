//go:build linux

package cli

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

func readSELinuxContext(path string) (string, error) {
	file, err := openShadow(path)
	if err != nil {
		return "", fmt.Errorf("open your regular file without symbolic links: %w", err)
	}
	defer file.Close()
	data := make([]byte, 4096)
	n, err := unix.Fgetxattr(int(file.Fd()), "security.selinux", data)
	switch {
	case errors.Is(err, unix.ENODATA):
		return "", errors.New("your file has no security.selinux label; this does not prove that your guest disables SELinux")
	case errors.Is(err, unix.ENOTSUP):
		return "", errors.New("your mounted filesystem does not expose SELinux extended attributes")
	case errors.Is(err, unix.ERANGE):
		return "", errors.New("your SELinux context exceeds the 4096-byte limit")
	case err != nil:
		return "", fmt.Errorf("read security.selinux: %w", err)
	}
	return string(data[:n]), nil
}
