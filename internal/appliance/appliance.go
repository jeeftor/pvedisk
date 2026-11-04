// Package appliance reconstructs Linux guests using one libguestfs handle.
package appliance

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
)

// Disk is an explicitly formatted disk image or local block volume.
type Disk struct{ Path, Format string }

// Filesystem records a discovered filesystem, including unmounted containers.
type Filesystem struct {
	Device, Type, Mountpoint, ID string
	Mounted                      bool
	Extra                        bool // Mounted separately from the guest's reconstructed hierarchy.
}

// Inventory describes the single selected Linux operating system and all filesystems.
type Inventory struct {
	Root        string
	Filesystems []Filesystem
}

type backend interface {
	Inspect_os() ([]string, error)
	Inspect_get_type(string) (string, error)
	Inspect_get_mountpoints(string) (map[string]string, error)
	List_filesystems() (map[string]string, error)
	Mkmountpoint(string) error
	Mount_options(string, string, string) error
	Umount_all() error
	Sync() error
	Shutdown() error
	Get_pid() (int, error)
	Mount_local_run() error
}

// Handle owns one appliance and its FUSE loop. Calls must be serialized; Close
// is only valid after Run has returned following a normal FUSE unmount.
type Handle struct {
	g                          backend
	readonly                   bool
	export                     func(string, bool, string) error
	close                      func() error
	prepared, exported, closed bool
}

// Prepare reconstructs the inspected hierarchy under /root and extra filesystems
// under /extras inside the appliance. No mountpoint directories are created in
// the guest: missing directories cause explicit mount failures.
func (h *Handle) Prepare() (Inventory, error) {
	var inventory Inventory
	if h.prepared || h.closed {
		return inventory, errors.New("appliance is already prepared or closed")
	}
	filesystems, err := h.g.List_filesystems()
	if err != nil {
		return inventory, fmt.Errorf("list filesystems: %w", err)
	}
	devices := make([]string, 0, len(filesystems))
	for device, kind := range filesystems {
		if strings.Contains(strings.ToLower(kind), "crypto") || strings.Contains(strings.ToLower(kind), "luks") || strings.Contains(strings.ToLower(kind), "bitlocker") {
			return inventory, fmt.Errorf("encrypted filesystem %s (%s) is unsupported", device, kind)
		}
		devices = append(devices, device)
	}
	sort.Strings(devices)
	roots, err := h.g.Inspect_os()
	if err != nil {
		return inventory, fmt.Errorf("inspect operating systems: %w", err)
	}
	if len(roots) != 1 {
		return inventory, fmt.Errorf("expected exactly one operating system, found %d", len(roots))
	}
	kind, err := h.g.Inspect_get_type(roots[0])
	if err != nil {
		return inventory, fmt.Errorf("inspect operating system type: %w", err)
	}
	if kind != "linux" {
		return inventory, fmt.Errorf("unsupported operating system %q", kind)
	}
	inventory.Root = roots[0]
	points, err := h.g.Inspect_get_mountpoints(roots[0])
	if err != nil {
		return inventory, fmt.Errorf("inspect mountpoints: %w", err)
	}
	if points["/"] == "" {
		return inventory, errors.New("inspection returned no root filesystem")
	}
	// libguestfs's NEED_ROOT gate accepts any mount under its internal
	// sysroot, including /root; a guest filesystem need not occupy "/".
	if err = h.g.Mkmountpoint("/root"); err != nil {
		return inventory, fmt.Errorf("create internal root mountpoint: %w", err)
	}
	if err = h.g.Mkmountpoint("/extras"); err != nil {
		return inventory, fmt.Errorf("create internal extras mountpoint: %w", err)
	}
	ordered := make([]string, 0, len(points))
	for point := range points {
		if !path.IsAbs(point) || path.Clean(point) != point {
			return inventory, fmt.Errorf("invalid inspected mountpoint %q", point)
		}
		ordered = append(ordered, point)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if len(ordered[i]) == len(ordered[j]) {
			return ordered[i] < ordered[j]
		}
		return len(ordered[i]) < len(ordered[j])
	})
	options := "rw"
	if h.readonly {
		options = "ro"
	}
	mounted := make(map[string]string)
	for _, point := range ordered {
		device := points[point]
		target := "/root"
		if point != "/" {
			target += point
		}
		if err = h.g.Mount_options(options, device, target); err != nil {
			return inventory, fmt.Errorf("mount %s at %s: %w", device, point, err)
		}
		mounted[device] = point
	}
	for _, device := range devices {
		fs := Filesystem{Device: device, Type: filesystems[device], ID: filesystemID(device)}
		if point, ok := mounted[device]; ok {
			fs.Mountpoint = point
			fs.Mounted = true
		} else if mountable(fs.Type) {
			target := "/extras/" + fs.ID
			if err = h.g.Mkmountpoint(target); err != nil {
				return inventory, fmt.Errorf("create extras mountpoint: %w", err)
			}
			if err = h.g.Mount_options(options, device, target); err != nil {
				return inventory, fmt.Errorf("mount extra filesystem %s (%s): %w", device, fs.Type, err)
			}
			fs.Mountpoint = target
			fs.Mounted = true
			fs.Extra = true
		}
		inventory.Filesystems = append(inventory.Filesystems, fs)
	}
	h.prepared = true
	return inventory, nil
}

func filesystemID(device string) string {
	sum := sha256.Sum256([]byte(device))
	return fmt.Sprintf("fs-%x", sum[:8])
}
func mountable(kind string) bool {
	return kind != "" && kind != "unknown" && kind != "swap" && !strings.Contains(strings.ToLower(kind), "member") && !strings.Contains(strings.ToLower(kind), "partition")
}

// Export prepares a FUSE mount labeled with the session ownership token.
// Run must then serve it. The label permits recovery to distinguish a recorded
// mount from an unrelated mount that subsequently occupies the same path.
func (h *Handle) Export(staging, ownership string) error {
	if !h.prepared || h.exported || h.closed {
		return errors.New("appliance must be prepared once before export")
	}
	if !validOwnership(ownership) {
		return errors.New("FUSE export requires an alphanumeric session ownership token")
	}
	if err := h.export(staging, h.readonly, ownership); err != nil {
		return fmt.Errorf("export appliance: %w", err)
	}
	h.exported = true
	return nil
}

func validOwnership(token string) bool {
	if len(token) == 0 || len(token) > 128 {
		return false
	}
	for _, c := range token {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// Run serves FUSE requests until an external normal unmount ends the session.
func (h *Handle) Run() error {
	if !h.exported || h.closed {
		return errors.New("appliance is not exported")
	}
	return h.g.Mount_local_run()
}

// PID returns the appliance process identity for persistent ownership checks.
func (h *Handle) PID() (int, error) { return h.g.Get_pid() }

// Close finalizes guest writes before closing. Any error preserves the handle
// so the caller retains protection and can retry or record unresolved state.
func (h *Handle) Close() error {
	if h.closed {
		return nil
	}
	if err := h.g.Umount_all(); err != nil {
		return fmt.Errorf("unmount guest filesystems: %w", err)
	}
	if err := h.g.Sync(); err != nil {
		return fmt.Errorf("synchronize appliance: %w", err)
	}
	if err := h.g.Shutdown(); err != nil {
		return fmt.Errorf("shutdown appliance: %w", err)
	}
	if err := h.close(); err != nil {
		return fmt.Errorf("close appliance: %w", err)
	}
	h.closed = true
	return nil
}
