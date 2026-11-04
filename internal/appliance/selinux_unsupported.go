//go:build !linux || !cgo

package appliance

import "errors"

// SELinuxConfig requires your Linux CGO build with native libguestfs.
func SELinuxConfig(_ []Disk) ([]byte, error) {
	return nil, errors.New("SELinux configuration inspection requires the Linux CGO build with native libguestfs")
}
