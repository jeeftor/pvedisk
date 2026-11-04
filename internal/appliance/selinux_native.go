//go:build linux && cgo

package appliance

import (
	"errors"
	"fmt"
	"io/fs"
	"syscall"

	"libguestfs.org/guestfs"
)

// SELinuxConfig reads the guest's /etc/selinux/config using a new read-only
// appliance and validates the /.autorelabel target. Your caller must first stop
// the VM, acquire owned startup protection and verify the disks are unused.
// Data is returned only after the appliance has shut down successfully. This
// function never executes guest programs or exports a FUSE mount.
func SELinuxConfig(disks []Disk) (data []byte, err error) {
	h, err := Open(disks, true)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := h.Close(); closeErr != nil {
			clear(data)
			data = nil
			err = errors.Join(err, fmt.Errorf("finalize SELinux configuration probe: %w", closeErr))
		}
	}()
	if _, err := h.Prepare(); err != nil {
		return nil, err
	}
	g := h.g.(*guestfs.Guestfs)
	stat := func(path string) (guestFileMetadata, error) {
		info, err := g.Lstatns(path)
		if err != nil {
			var native *guestfs.GuestfsError
			if errors.As(err, &native) && native.Errno == syscall.ENOENT {
				return guestFileMetadata{}, fs.ErrNotExist
			}
			return guestFileMetadata{}, err
		}
		return guestFileMetadata{mode: info.St_mode, size: info.St_size}, nil
	}
	return readSELinuxConfig(stat, g.Pread)
}
