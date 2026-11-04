//go:build linux && cgo && integration

package appliance

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"libguestfs.org/guestfs"
)

// TestDiskImage verifies real inspection, FUSE writes, write persistence and
// kernel read-only enforcement against a disposable disk, without Proxmox.
func TestDiskImage(t *testing.T) {
	const selinuxConfig = "SELINUX=enforcing\nSELINUXTYPE=targeted\n"
	const selinuxContext = "system_u:object_r:etc_t:s0\x00"
	const shadow = "root:!:20000:0:99999:7:::\n"
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
		func() error { return g.Mkdir("/etc/selinux") },
		func() error { return g.Write("/etc/selinux/config", []byte(selinuxConfig)) },
		func() error { return g.Write("/etc/shadow", []byte(shadow)) },
		func() error {
			return g.Lsetxattr("security.selinux", selinuxContext, len(selinuxContext), "/etc/selinux/config")
		},
		func() error { return g.Mkdir("/bin") },
		func() error {
			// virt-customize can attach -a disks in a different order from our
			// native handle. Identify the guest root independently of /dev/sdX.
			uuid, err := g.Vfs_uuid("/dev/sda1")
			if err != nil {
				return err
			}
			if uuid == "" {
				return fmt.Errorf("fixture root filesystem has no UUID")
			}
			return g.Write("/etc/fstab", []byte("UUID="+uuid+" / ext4 defaults 0 1\n"))
		},
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
	config, err := SELinuxConfig([]Disk{{Path: image, Format: "raw"}, {Path: extra, Format: "raw"}})
	if err != nil || string(config) != selinuxConfig {
		t.Fatalf("read-only SELinux configuration probe: data=%q error=%v", config, err)
	}
	customize := exec.Command("virt-customize", "--format", "raw", "-a", image, "--format", "raw", "-a", extra,
		"--no-network", "--no-selinux-relabel", "--touch", "/.autorelabel")
	customize.Env = append(os.Environ(), "LIBGUESTFS_BACKEND=direct")
	if output, err := customize.CombinedOutput(); err != nil {
		t.Fatalf("schedule SELinux relabel using native virt-customize: %v: %s", err, output)
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
		if !readonly {
			native := h.g.(*guestfs.Guestfs)
			marker, err := native.Lstatns("/root/.autorelabel")
			if err != nil || marker.St_mode&0170000 != 0100000 {
				t.Fatalf("virt-customize did not persist a regular /.autorelabel marker: %v", err)
			}
			passwordFile, err := native.Read_file("/root/etc/shadow")
			if err != nil || string(passwordFile) != shadow {
				t.Fatalf("SELinux scheduling changed the shadow file or made it unreadable: %v", err)
			}
		}
		extraID := ""
		for _, fs := range inventory.Filesystems {
			if fs.Device == "/dev/sdb" {
				if !fs.Mounted || !fs.Extra || fs.Mountpoint != "/extras/"+fs.ID {
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
			file, err := os.Open(filepath.Join(staging, "root", "etc", "selinux", "config"))
			if err != nil {
				t.Fatal(err)
			}
			contextBuffer := make([]byte, 4096)
			n, contextErr := unix.Fgetxattr(int(file.Fd()), "security.selinux", contextBuffer)
			file.Close()
			if contextErr != nil || string(contextBuffer[:n]) != selinuxContext {
				t.Fatalf("SELinux context through native FUSE: context=%q error=%v", contextBuffer[:max(n, 0)], contextErr)
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
