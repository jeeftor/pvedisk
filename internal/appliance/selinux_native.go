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
	inventory, err := h.Prepare()
	if err != nil {
		return nil, err
	}
	g := h.g.(*guestfs.Guestfs)
	stat := func(path string) (guestFileMetadata, error) { return statGuestFile(g, path) }
	volumes, err := g.Lvs()
	if err != nil {
		return nil, fmt.Errorf("identify guest logical volumes before customization: %w", err)
	}
	if err := verifyCustomizationRoot(inventory, volumes); err != nil {
		return nil, err
	}
	if len(disks) > 1 {
		if err := readCustomizationFstab(stat, g.Pread, customizationLogicalVolumes(volumes)); err != nil {
			return nil, err
		}
	}
	return readSELinuxConfig(stat, g.Pread)
}

// CheckCustomizationMounts verifies that a multi-disk guest uses stable fstab
// device identities before virt-customize can reorder its disks. Single-disk
// guests are unaffected. Your caller must hold owned startup protection with
// the VM stopped and disks unused throughout this read-only probe.
func CheckCustomizationMounts(disks []Disk) (err error) {
	if len(disks) <= 1 {
		return nil
	}
	h, err := Open(disks, true)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := h.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("finalize customization mount probe: %w", closeErr))
		}
	}()
	inventory, err := h.Prepare()
	if err != nil {
		return err
	}
	g := h.g.(*guestfs.Guestfs)
	volumes, err := g.Lvs()
	if err != nil {
		return fmt.Errorf("identify guest logical volumes before customization: %w", err)
	}
	if err := verifyCustomizationRoot(inventory, volumes); err != nil {
		return err
	}
	return readCustomizationFstab(func(path string) (guestFileMetadata, error) { return statGuestFile(g, path) }, g.Pread, customizationLogicalVolumes(volumes))
}

func statGuestFile(g *guestfs.Guestfs, path string) (guestFileMetadata, error) {
	if path == "/root/.autorelabel" {
		// Exists uses lstat, so a dangling link still reaches the type check.
		exists, err := g.Exists(path)
		if err != nil {
			return guestFileMetadata{}, err
		}
		if !exists {
			return guestFileMetadata{}, fs.ErrNotExist
		}
	}
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
