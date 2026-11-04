package appliance

import (
	"errors"
	"fmt"
	"io/fs"
)

const maxSELinuxConfigSize = 64 * 1024

type guestFileMetadata struct {
	mode, size int64
}

// readSELinuxConfig treats guest configuration as data and rejects links before
// any path-following read. The caller keeps guest disks offline and protected.
func readSELinuxConfig(stat func(string) (guestFileMetadata, error), read func(string, int, int64) ([]byte, error)) ([]byte, error) {
	for _, path := range []string{"/root", "/root/etc", "/root/etc/selinux"} {
		info, err := stat(path)
		if err != nil {
			return nil, fmt.Errorf("inspect guest SELinux configuration directory: %w", err)
		}
		if info.mode&0170000 != 0040000 {
			return nil, errors.New("your guest SELinux configuration directories must be directories without symbolic links")
		}
	}
	const config = "/root/etc/selinux/config"
	info, err := stat(config)
	if err != nil {
		return nil, fmt.Errorf("inspect guest /etc/selinux/config: %w", err)
	}
	if info.mode&0170000 != 0100000 {
		return nil, errors.New("your guest /etc/selinux/config must be a regular file without symbolic links")
	}
	if info.size < 0 || info.size > maxSELinuxConfigSize {
		return nil, errors.New("your guest SELinux configuration exceeds the 64-kilobyte limit")
	}
	// The later scheduling operation touches this path. Reject a pre-existing
	// link or special file rather than letting touch follow an ambiguous target.
	marker, err := stat("/root/.autorelabel")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("inspect guest /.autorelabel: %w", err)
	}
	if err == nil && marker.mode&0170000 != 0100000 {
		return nil, errors.New("your guest /.autorelabel must be absent or a regular file without symbolic links")
	}
	data, err := read(config, maxSELinuxConfigSize+1, 0)
	if err != nil {
		clear(data)
		return nil, fmt.Errorf("read guest SELinux configuration: %w", err)
	}
	if len(data) > maxSELinuxConfigSize {
		clear(data)
		return nil, errors.New("your guest SELinux configuration exceeds the 64-kilobyte limit")
	}
	return data, nil
}
