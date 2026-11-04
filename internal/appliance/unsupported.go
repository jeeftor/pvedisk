//go:build !linux || !cgo

package appliance

import "errors"

// Open requires the Linux CGO build shipped for Debian and Proxmox hosts.
func Open(_ []Disk, _ bool) (*Handle, error) {
	return nil, errors.New("disk access requires the Linux CGO build with native libguestfs")
}
