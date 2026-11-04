package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/jeeftor/pvedisk/internal/appliance"
	"github.com/jeeftor/pvedisk/internal/config"
	"github.com/jeeftor/pvedisk/internal/session"
)

func TestMountListsFullHostPathsForGuestAndExtraFilesystems(t *testing.T) {
	state := &session.State{Public: "/srv/repair/vm406", Extras: "/srv/repair/vm406-extra", Inventory: appliance.Inventory{Filesystems: []appliance.Filesystem{
		{Device: "/dev/vg/root", Type: "xfs", Mountpoint: "/", Mounted: true},
		{Device: "/dev/sda2", Type: "ext4", Mountpoint: "/boot", Mounted: true},
		{Device: "/dev/sda1", Type: "vfat", Mountpoint: "/boot/efi", Mounted: true},
		{Device: "/dev/sdb1", Type: "ext4", Mountpoint: "/extras/fs-extra", ID: "fs-extra", Mounted: true, Extra: true},
		{Device: "/dev/sda3", Type: "swap"},
		{Device: "/dev/sda4", Type: "LVM2_member"},
	}}}
	ops := testOperations()
	ops.load = func(string) (config.Config, error) { return config.Config{MountRoot: "/srv/repair"}, nil }
	ops.mount = func(context.Context, int, string, bool, func(string)) error { return nil }
	ops.status = func(int) (*session.State, error) { return state, nil }
	output, err := execute(ops, "mount", "406")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "HOST PATH") || !strings.Contains(output, "Additional filesystems:") {
		t.Fatalf("missing filesystem path labels: %q", output)
	}
	for device, expected := range map[string]string{
		"/dev/vg/root": "/srv/repair/vm406",
		"/dev/sda2":    "/srv/repair/vm406/boot",
		"/dev/sda1":    "/srv/repair/vm406/boot/efi",
		"/dev/sdb1":    "/srv/repair/vm406-extra/fs-extra",
		"/dev/sda3":    "—",
		"/dev/sda4":    "—",
	} {
		found := false
		for _, line := range strings.Split(output, "\n") {
			if strings.HasPrefix(line, device+" ") {
				found = true
				if !strings.HasSuffix(strings.TrimSpace(line), expected) {
					t.Fatalf("wrong host path for %s: %q", device, line)
				}
				if expected == "—" && (!strings.Contains(line, "not mounted") || strings.Contains(line, "/srv/repair")) {
					t.Fatalf("unmounted filesystem claims a host path: %q", line)
				}
			}
		}
		if !found {
			t.Fatalf("missing filesystem %s in %q", device, output)
		}
	}
	if strings.Contains(output, "╭") || strings.Contains(output, "╰") || strings.Contains(output, "\x1b") {
		t.Fatalf("redirected summary has decorations: %q", output)
	}
}

func TestMountHostPathUsesExplicitExtraClassification(t *testing.T) {
	state := &session.State{Public: "/repair/vm406", Extras: "/repair/vm406-extra"}
	guest := appliance.Filesystem{Mounted: true, Mountpoint: "/extras/fs-123", ID: "fs-123"}
	extra := guest
	extra.Extra = true
	if got := filesystemHostPath(state, guest); got != "/repair/vm406/extras/fs-123" {
		t.Fatalf("guest mountpoint misclassified as extra: %q", got)
	}
	if got := filesystemHostPath(state, extra); got != "/repair/vm406-extra/fs-123" {
		t.Fatalf("extra filesystem mapped into guest tree: %q", got)
	}
	guest.Mounted = false
	extra.Mounted = false
	for _, fs := range []appliance.Filesystem{guest, extra} {
		if got := filesystemHostPath(state, fs); got != "—" {
			t.Fatalf("unmounted filesystem has host path: %q", got)
		}
	}
}

func TestMountSummaryHidesUnusedExtrasAndEmptyBox(t *testing.T) {
	state := &session.State{ReadOnly: true, Public: "/repair/vm406", Extras: "/repair/vm406-extra", Inventory: appliance.Inventory{Filesystems: []appliance.Filesystem{{Device: "/dev/sda1", Type: "ext4", Mountpoint: "/", Mounted: true}}}}
	var output strings.Builder
	if err := writeMountSummary(&output, 406, state); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if strings.Contains(text, "Additional filesystems:") || strings.Contains(text, "vm406-extra") || strings.Contains(text, "╭") {
		t.Fatalf("summary contains an empty extras section or box: %q", text)
	}
	for _, want := range []string{"Your VM 406 is mounted (read-only).", "Guest root: /repair/vm406", "HOST PATH", "pvedisk unmount 406"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %q", want, text)
		}
	}
}

func TestMountHostPathDoesNotInventPathsForInvalidMetadata(t *testing.T) {
	state := &session.State{Public: "/repair/vm406", Extras: "/repair/vm406-extra"}
	for _, fs := range []appliance.Filesystem{
		{Mounted: true, Mountpoint: "boot"},
		{Mounted: true, Mountpoint: "/../escape"},
		{Mounted: true, Extra: true, ID: "../escape"},
		{Mounted: true, Extra: true, ID: ""},
	} {
		if got := filesystemHostPath(state, fs); got != "—" {
			t.Fatalf("invalid filesystem metadata has a host path: %q", got)
		}
	}
}
