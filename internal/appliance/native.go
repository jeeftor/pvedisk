//go:build linux && cgo

package appliance

import (
	"errors"
	"fmt"

	"libguestfs.org/guestfs"
)

// Open launches a direct libguestfs appliance with explicitly formatted drives.
func Open(disks []Disk, readOnly bool) (*Handle, error) {
	if len(disks) == 0 {
		return nil, errors.New("no disks supplied")
	}
	g, err := guestfs.Create()
	if err != nil {
		return nil, fmt.Errorf("create libguestfs handle: %w", err)
	}
	fail := func(err error) (*Handle, error) { g.Close(); return nil, err }
	if err := g.Set_backend("direct"); err != nil {
		return fail(fmt.Errorf("select direct backend: %w", err))
	}
	for _, disk := range disks {
		if disk.Path == "" || (disk.Format != "raw" && disk.Format != "qcow2" && disk.Format != "vmdk") {
			return fail(fmt.Errorf("disk %q needs an explicit supported format", disk.Path))
		}
		if err := g.Add_drive(disk.Path, &guestfs.OptargsAdd_drive{Readonly_is_set: true, Readonly: readOnly, Format_is_set: true, Format: disk.Format}); err != nil {
			return fail(fmt.Errorf("add disk %s: %w", disk.Path, err))
		}
	}
	if err := g.Launch(); err != nil {
		return fail(fmt.Errorf("launch libguestfs: %w", err))
	}
	return &Handle{g: g, readonly: readOnly, export: func(staging string, ro bool, ownership string) error {
		options := "default_permissions,fsname=pvedisk-" + ownership + ",subtype=guestfs"
		return g.Mount_local(staging, &guestfs.OptargsMount_local{Readonly_is_set: true, Readonly: ro, Options_is_set: true, Options: options})
	}, close: func() error {
		if err := g.Close(); err != nil {
			return err
		}
		return nil
	}}, nil
}
