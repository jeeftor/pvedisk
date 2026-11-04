package appliance

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
)

func TestCustomizationAcceptsStableFstabIdentities(t *testing.T) {
	for _, source := range []string{
		"UUID=1234-5678", "LABEL=root", "PARTUUID=1234", "PARTLABEL=system", "LABEL=root\\040volume",
		"/dev/mapper/vg-root", "/dev/vg/root", "/dev/disk/by-uuid/1234", "/dev/disk/by-label/root",
		"/dev/disk/by-partuuid/1234", "/dev/disk/by-partlabel/root", "tmpfs", "proc", "none", "server:/export", "/swapfile",
	} {
		t.Run(source, func(t *testing.T) {
			if err := checkCustomizationFstab([]byte("# ignored /dev/sda1\n"+source+" / ext4 defaults 0 1 # comment\n"), customizationLogicalVolumes([]string{"/dev/vg/root"})); err != nil {
				t.Fatalf("stable source rejected: %v", err)
			}
		})
	}
}

func TestCustomizationRejectsPositionalAndUnknownDevices(t *testing.T) {
	for _, source := range []string{
		"/dev/sda1", "/dev/sdz", "/dev/vda2", "/dev/xvda1", "/dev/hdb3", "/dev/nvme0n1", "/dev/nvme2n3p4",
		"/dev/mmcblk0", "/dev/mmcblk1p2", "/dev/root", "/dev/unknown", "/dev/loop0", "/dev/disk/by-id/device", "/dev/disk/by-path/device",
		"/dev/disk", "/dev", "/./dev", "/dev/mapper/../sda1", "/dev/vg/root/extra", "\\057dev\\057sda1", "/dev/\\163da1",
		"/dev/block/8:1", "/dev/fd/3", "/dev/arbitrary/device", "/dev/mapper/unknown", "/./dev/block/8:1",
	} {
		t.Run(source, func(t *testing.T) {
			data := "UUID=root / ext4 defaults 0 1\n" + source + " /data ext4 defaults 0 2\n"
			if err := checkCustomizationFstab([]byte(data), nil); !errors.Is(err, ErrUnsafeCustomizationMounts) {
				t.Fatalf("unsafe additional mount accepted: %v", err)
			}
		})
	}
}

func TestCustomizationRejectsMalformedFstab(t *testing.T) {
	for _, data := range []string{"/dev/sda1", "UUID=root # no mountpoint", "\x00", "UUID=bad\\0 / ext4 defaults", "UUID=bad\\777 / ext4 defaults", "\"/dev/sda1\" / ext4 defaults", "'/dev/sda1' / ext4 defaults", "\\042/dev/sda1\\042 / ext4 defaults", strings.Repeat("x", maxCustomizationFstabSize+1)} {
		if err := checkCustomizationFstab([]byte(data), nil); err == nil {
			t.Fatal("malformed fstab was accepted")
		}
	}
}

func TestCustomizationFstabReadRejectsUnsafeOrOversizedFiles(t *testing.T) {
	for _, name := range []string{"/root", "/root/etc", "/root/etc/fstab"} {
		files := map[string]guestFileMetadata{"/root": {mode: 0040755}, "/root/etc": {mode: 0040755}, "/root/etc/fstab": {mode: 0100644}}
		files[name] = guestFileMetadata{mode: 0120777}
		if err := readCustomizationFstab(fixtureGuestStat(files), func(string, int, int64) ([]byte, error) { t.Fatal("symlink was followed"); return nil, nil }, nil); err == nil {
			t.Fatal("unsafe fstab path accepted")
		}
	}
	files := map[string]guestFileMetadata{"/root": {mode: 0040755}, "/root/etc": {mode: 0040755}, "/root/etc/fstab": {mode: 0100644}}
	if err := readCustomizationFstab(fixtureGuestStat(files), func(path string, count int, offset int64) ([]byte, error) {
		if path != "/root/etc/fstab" || count != maxCustomizationFstabSize+1 || offset != 0 {
			t.Fatal("unbounded fstab read")
		}
		return []byte("UUID=root / ext4 defaults 0 1\n"), nil
	}, nil); err != nil {
		t.Fatal(err)
	}
	delete(files, "/root/etc/fstab")
	if err := readCustomizationFstab(fixtureGuestStat(files), nil, nil); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing fstab accepted: %v", err)
	}
}

func TestCustomizationUsesKnownLogicalVolumeIdentities(t *testing.T) {
	allowed := customizationLogicalVolumes([]string{"/dev/vg-data/lv-root"})
	for _, source := range []string{"/dev/vg-data/lv-root", "/dev/mapper/vg--data-lv--root"} {
		if err := checkCustomizationFstab([]byte(source+" / ext4 defaults 0 1\n"), allowed); err != nil {
			t.Fatalf("known logical volume rejected: %v", err)
		}
	}
	for _, source := range []string{"/dev/vg-data/other", "/dev/mapper/vg-data-lv-root", "/dev/mapper/vg--data-other", "/dev/vg-data/../sda1", "/dev/block/8:1"} {
		if err := checkCustomizationFstab([]byte(source+" / ext4 defaults 0 1\n"), allowed); !errors.Is(err, ErrUnsafeCustomizationMounts) {
			t.Fatalf("unproven logical volume accepted: %v", err)
		}
	}
}

func TestSingleDiskCustomizationDoesNotNeedAProbe(t *testing.T) {
	if err := CheckCustomizationMounts([]Disk{{Path: "/not-opened", Format: "raw"}}); err != nil {
		t.Fatal("single disk unnecessarily probed")
	}
}

func TestCustomizationVerifiesTheInspectedRoot(t *testing.T) {
	for _, tc := range []struct {
		name, inspected, mounted string
		volumes                  []string
		valid                    bool
	}{
		{"matching partition", "/dev/sda1", "/dev/sda1", nil, true},
		{"known logical-volume aliases", "/dev/vg-data/lv-root", "/dev/mapper/vg--data-lv--root", []string{"/dev/vg-data/lv-root"}, true},
		{"reverse known aliases", "/dev/mapper/vg--data-lv--root", "/dev/vg-data/lv-root", []string{"/dev/vg-data/lv-root"}, true},
		{"different logical volume", "/dev/vg/root", "/dev/mapper/vg-data", []string{"/dev/vg/root", "/dev/vg/data"}, false},
		{"unproven logical alias", "/dev/vg/root", "/dev/mapper/vg-root", nil, false},
		{"positional data disk", "/dev/sdb1", "/dev/sda1", nil, false},
		{"missing root identity", "", "/dev/sda1", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inventory := Inventory{Root: tc.inspected, Filesystems: []Filesystem{{Device: tc.mounted, Mountpoint: "/", Mounted: true}}}
			if err := verifyCustomizationRoot(inventory, tc.volumes); (err == nil) != tc.valid {
				t.Fatalf("root verification: %v", err)
			}
		})
	}
	for _, filesystems := range [][]Filesystem{nil, {{Device: "/dev/sda1", Mountpoint: "/", Mounted: false}}, {{Device: "/dev/sda1", Mountpoint: "/", Mounted: true}, {Device: "/dev/sda1", Mountpoint: "/", Mounted: true}}} {
		if err := verifyCustomizationRoot(Inventory{Root: "/dev/sda1", Filesystems: filesystems}, nil); err == nil {
			t.Fatal("missing or ambiguous mounted root accepted")
		}
	}
}
