package session

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jeeftor/pvedisk/internal/appliance"
)

// ScheduleSELinuxRelabel requests a relabel on the guest's next boot. Disks must
// be unmounted; startup protection remains held until all offline work finishes.
// This never enables SELinux or starts the guest.
func (m *Manager) ScheduleSELinuxRelabel(ctx context.Context, id int, configPath string) error {
	return m.offlineMaintenance(ctx, id, configPath, "selinux-relabel", func(s *State) error {
		disks := make([]appliance.Disk, 0, len(s.Disks))
		for _, disk := range s.Disks {
			disks = append(disks, appliance.Disk{Path: disk.Path, Format: disk.Format})
		}
		data, err := appliance.SELinuxConfig(disks)
		defer clear(data)
		if err != nil {
			return fmt.Errorf("inspect your guest SELinux configuration: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := validateSELinuxConfig(data); err != nil {
			return err
		}
		// Disable customization's automatic immediate relabel: this utility only
		// schedules the explicit marker, using the guest policy on its next boot.
		if err := runCustomization(ctx, s, "--no-network", "--no-selinux-relabel", "--touch", "/.autorelabel"); err != nil {
			return fmt.Errorf("schedule your guest SELinux relabel; inspect and clean up your session: %w", err)
		}
		return nil
	})
}

func validateSELinuxConfig(data []byte) error {
	var mode string
	found := false
	for _, line := range strings.Split(string(data), "\n") {
		line, _, _ = strings.Cut(line, "#")
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "SELINUX" {
			continue
		}
		if found {
			return errors.New("your guest SELinux configuration has conflicting SELINUX assignments")
		}
		found = true
		mode = strings.TrimSpace(value)
		if len(mode) >= 2 && (mode[0] == '\'' || mode[0] == '"') && mode[len(mode)-1] == mode[0] {
			mode = mode[1 : len(mode)-1]
		}
	}
	if !found || (mode != "enforcing" && mode != "permissive") {
		return errors.New("your guest must already configure SELINUX=enforcing or SELINUX=permissive; this utility does not enable SELinux")
	}
	return nil
}
