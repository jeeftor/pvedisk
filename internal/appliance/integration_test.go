//go:build linux && cgo && integration

package appliance

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"libguestfs.org/guestfs"
)

// TestDiskImage verifies real inspection, FUSE writes, write persistence and
// kernel read-only enforcement against a disposable disk, without Proxmox.
func TestDiskImage(t *testing.T) {
	dir := t.TempDir()
	image := filepath.Join(dir, "guest.raw")
	extra := filepath.Join(dir, "extra.raw")
	for _, imagePath := range []string{image, extra} {
		file, err := os.Create(imagePath)
		if err != nil {
			t.Fatal(err)
		}
		if err = file.Truncate(256 << 20); err != nil {
			file.Close()
			t.Fatal(err)
		}
		if err = file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	g, createErr := guestfs.Create()
	if createErr != nil {
		t.Fatal(createErr)
	}
	defer g.Close()
	steps := []func() error{
		func() error { return g.Set_backend("direct") },
		func() error { return g.Add_drive(image, &guestfs.OptargsAdd_drive{Format_is_set: true, Format: "raw"}) },
		func() error { return g.Add_drive(extra, &guestfs.OptargsAdd_drive{Format_is_set: true, Format: "raw"}) },
		g.Launch,
		func() error { return g.Part_disk("/dev/sda", "mbr") },
		func() error { return g.Mkfs("ext4", "/dev/sda1", nil) },
		func() error { return g.Mkfs("ext4", "/dev/sdb", nil) },
		func() error { return g.Mount("/dev/sda1", "/") },
		func() error { return g.Mkdir("/etc") },
		func() error { return g.Mkdir("/bin") },
		func() error { return g.Write("/etc/fstab", []byte("/dev/sda1 / ext4 defaults 0 1\n")) },
		func() error {
			return g.Write("/etc/os-release", []byte("NAME=Debian\nID=debian\nVERSION_ID=12\nPRETTY_NAME=Debian\n"))
		},
		func() error { return g.Write("/etc/debian_version", []byte("12.0\n")) },
		func() error { return g.Write("/bin/sh", []byte("test shell marker\n")) },
		g.Umount_all, g.Sync, g.Shutdown,
	}
	for i, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("seed step %d: %v", i, err)
		}
	}
	for _, readonly := range []bool{false, true} {
		h, err := Open([]Disk{{Path: image, Format: "raw"}, {Path: extra, Format: "raw"}}, readonly)
		if err != nil {
			t.Fatal(err)
		}
		inventory, err := h.Prepare()
		if err != nil {
			h.Close()
			t.Fatal(err)
		}
		if inventory.Root == "" {
			t.Fatal("no root detected")
		}
		extraID := ""
		for _, fs := range inventory.Filesystems {
			if fs.Device == "/dev/sdb" {
				if !fs.Mounted || fs.Mountpoint != "/extras/"+fs.ID {
					t.Fatalf("extra filesystem was not exposed: %+v", fs)
				}
				extraID = fs.ID
			}
		}
		if extraID == "" {
			t.Fatal("extra filesystem missing from inventory")
		}
		staging, err := os.MkdirTemp(dir, "staging-")
		if err != nil {
			t.Fatal(err)
		}
		if err = h.Export(staging, "integration-session"); err != nil {
			h.Close()
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- h.Run() }()
		func() {
			defer func() {
				unmount, lookupErr := exec.LookPath("fusermount3")
				if lookupErr != nil {
					unmount, lookupErr = exec.LookPath("fusermount")
				}
				if lookupErr != nil {
					t.Fatal(lookupErr)
				}
				if out, err := exec.Command(unmount, "-u", staging).CombinedOutput(); err != nil {
					t.Fatalf("unmount: %v: %s", err, out)
				}
				select {
				case err := <-done:
					if err != nil {
						t.Error(err)
					}
				case <-time.After(30 * time.Second):
					t.Fatal("FUSE did not stop")
				}
				if err := h.Close(); err != nil {
					t.Fatal(err)
				}
			}()
			mountinfo, err := os.ReadFile("/proc/self/mountinfo")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(mountinfo), " - fuse.guestfs pvedisk-integration-session ") {
				t.Fatal("FUSE mount does not carry the expected recovery ownership label")
			}
			for _, target := range []string{filepath.Join(staging, "root", "persisted"), filepath.Join(staging, "extras", extraID, "persisted")} {
				if readonly {
					data, err := os.ReadFile(target)
					if err != nil || string(data) != "persisted edit\n" {
						t.Fatalf("persisted edit: %q, %v", data, err)
					}
					if err := os.WriteFile(target, []byte("must fail"), 0600); err == nil {
						t.Fatal("read-only export allowed a write")
					}
				} else {
					if err := os.WriteFile(target, []byte("persisted edit\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
		}()
	}
}
