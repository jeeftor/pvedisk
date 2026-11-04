package appliance

import (
	"bytes"
	"errors"
	"fmt"
	"path"
	"strings"
)

// ErrUnsafeCustomizationMounts prevents virt-customize from confusing disk
// identities when its appliance attaches multiple disks in a different order.
var ErrUnsafeCustomizationMounts = errors.New("your multi-disk guest fstab uses positional or unsupported device paths; use UUID, LABEL, PARTUUID, PARTLABEL, logical-volume, or stable filesystem identifiers before customization")

const maxCustomizationFstabSize = 64 * 1024

func checkCustomizationFstab(data []byte, logicalVolumes map[string]bool) error {
	if len(data) > maxCustomizationFstabSize || bytes.IndexByte(data, 0) >= 0 {
		return errors.New("your guest fstab must be bounded text without zero bytes")
	}
	for index, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		for i, field := range fields {
			if strings.HasPrefix(field, "#") {
				fields = fields[:i]
				break
			}
		}
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 2 {
			return fmt.Errorf("your guest fstab has an incomplete entry on line %d", index+1)
		}
		source, err := decodeFstabSource(fields[0])
		if err != nil {
			return fmt.Errorf("your guest fstab has an invalid source on line %d", index+1)
		}
		clean := path.Clean(source)
		if clean != "/dev" && !strings.HasPrefix(clean, "/dev/") {
			continue
		}
		if source != clean {
			return fmt.Errorf("fstab line %d: %w", index+1, ErrUnsafeCustomizationMounts)
		}
		parts := strings.Split(strings.TrimPrefix(source, "/dev/"), "/")
		allowed := logicalVolumes[source]
		if len(parts) == 3 && parts[0] == "disk" && parts[2] != "" {
			switch parts[1] {
			case "by-uuid", "by-label", "by-partuuid", "by-partlabel":
				allowed = true
			}
		}
		if !allowed {
			return fmt.Errorf("fstab line %d: %w", index+1, ErrUnsafeCustomizationMounts)
		}
	}
	return nil
}

func customizationLogicalVolumes(volumes []string) map[string]bool {
	aliases := customizationLogicalVolumeAliases(volumes)
	allowed := make(map[string]bool, len(aliases))
	for alias := range aliases {
		allowed[alias] = true
	}
	return allowed
}

func customizationLogicalVolumeAliases(volumes []string) map[string]string {
	aliases := make(map[string]string, len(volumes)*2)
	for _, volume := range volumes {
		if !strings.HasPrefix(volume, "/dev/") || path.Clean(volume) != volume {
			continue
		}
		parts := strings.Split(strings.TrimPrefix(volume, "/dev/"), "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			continue
		}
		aliases[volume] = volume
		// Device mapper doubles literal hyphens before joining VG and LV names.
		mapper := strings.ReplaceAll(parts[0], "-", "--") + "-" + strings.ReplaceAll(parts[1], "-", "--")
		aliases["/dev/mapper/"+mapper] = volume
	}
	return aliases
}

func verifyCustomizationRoot(inventory Inventory, volumes []string) error {
	aliases := customizationLogicalVolumeAliases(volumes)
	canonical := func(device string) string {
		if resolved := aliases[device]; resolved != "" {
			return resolved
		}
		return device
	}
	rootCount := 0
	for _, filesystem := range inventory.Filesystems {
		if !filesystem.Mounted || filesystem.Mountpoint != "/" {
			continue
		}
		rootCount++
		if filesystem.Extra || filesystem.Device == "" || inventory.Root == "" || canonical(filesystem.Device) != canonical(inventory.Root) {
			return errors.New("your inspected operating-system root differs from the reconstructed root filesystem; customization is refused")
		}
	}
	if rootCount != 1 {
		return errors.New("your inspected operating-system root cannot be verified against exactly one mounted root filesystem; customization is refused")
	}
	return nil
}

func decodeFstabSource(value string) (string, error) {
	var decoded strings.Builder
	for i := 0; i < len(value); i++ {
		b := value[i]
		if b == '\\' {
			if i+3 >= len(value) {
				return "", errors.New("incomplete escape")
			}
			var code int
			for j := 1; j <= 3; j++ {
				c := value[i+j]
				if c < '0' || c > '7' {
					return "", errors.New("invalid escape")
				}
				code = code*8 + int(c-'0')
			}
			if code > 255 {
				return "", errors.New("invalid escape")
			}
			b = byte(code)
			i += 3
		}
		if b == 0 || b == '\n' || b == '\r' || b == '\'' || b == '"' {
			return "", errors.New("invalid source character")
		}
		decoded.WriteByte(b)
	}
	return decoded.String(), nil
}

func readCustomizationFstab(stat func(string) (guestFileMetadata, error), read func(string, int, int64) ([]byte, error), logicalVolumes map[string]bool) error {
	for _, name := range []string{"/root", "/root/etc", "/root/etc/fstab"} {
		info, err := stat(name)
		if err != nil {
			return fmt.Errorf("inspect your guest fstab before multi-disk customization: %w", err)
		}
		kind := int64(0040000)
		if name == "/root/etc/fstab" {
			kind = 0100000
			if info.size < 0 || info.size > maxCustomizationFstabSize {
				return errors.New("your guest fstab exceeds the 64-kilobyte limit")
			}
		}
		if info.mode&0170000 != kind {
			return errors.New("your guest fstab must be a regular file reached through directories without symbolic links")
		}
	}
	data, err := read("/root/etc/fstab", maxCustomizationFstabSize+1, 0)
	defer clear(data)
	if err != nil {
		return fmt.Errorf("read your guest fstab before multi-disk customization: %w", err)
	}
	return checkCustomizationFstab(data, logicalVolumes)
}
