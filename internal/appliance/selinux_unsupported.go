//go:build !linux || !cgo

package appliance

import "errors"

// SELinuxConfig requires your Linux CGO build with native libguestfs.
func SELinuxConfig(_ []Disk) ([]byte, error) {
	return nil, errors.New("SELinux configuration inspection requires the Linux CGO build with native libguestfs")
}

// CheckCustomizationMounts requires native libguestfs for multi-disk probes.
func CheckCustomizationMounts(disks []Disk) error {
	if len(disks) <= 1 {
		return nil
	}
	return errors.New("multi-disk customization inspection requires the Linux CGO build with native libguestfs")
}
