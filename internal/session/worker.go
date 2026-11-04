package session

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jeeftor/pvedisk/internal/appliance"
	"github.com/jeeftor/pvedisk/internal/proxmox"
)

// Worker runs one persistent appliance until authenticated cleanup is requested.
func (m *Manager) Worker(ctx context.Context, id int) error {
	var s *State
	var h *appliance.Handle
	var listener net.Listener
	exported := false
	runDone := make(chan error, 1)
	err := m.locked(func() error {
		var err error
		s, err = m.Status(id)
		if err != nil {
			return err
		}
		if s.Phase != "pending" {
			return errors.New("your session is not pending worker startup")
		}
		s.Worker, err = identity(os.Getpid())
		if err != nil {
			return err
		}
		s.Phase = "inspecting"
		if err = m.save(s); err != nil {
			return err
		}
		p := proxmox.New()
		s.Disks, err = p.Inspect(ctx, id)
		if err != nil {
			return m.fail(s, err)
		}
		s.DiskIdentities, err = captureDiskIdentities(s.Disks)
		if err != nil {
			return m.fail(s, err)
		}
		s.Phase = "stopping"
		if err = m.save(s); err != nil {
			return err
		}
		if err = p.Shutdown(ctx, id); err != nil {
			return m.fail(s, err)
		}
		s.Phase = "acquiring"
		s.ProtectionAttempted = true
		if err = m.save(s); err != nil {
			return err
		}
		protectedDisks, err := p.Acquire(ctx, id, s.Token)
		if err != nil {
			return m.fail(s, err)
		}
		s.Disks = protectedDisks
		s.DiskIdentities, err = captureDiskIdentities(s.Disks)
		if err != nil {
			return m.fail(s, err)
		}
		s.Phase = "protected"
		if err = m.save(s); err != nil {
			return err
		}
		if err = assertDisksUnused(s.Disks); err != nil {
			return m.fail(s, err)
		}
		for _, path := range []string{s.Public, s.Extras, s.Staging} {
			if err = os.Mkdir(path, 0700); err != nil {
				return m.fail(s, fmt.Errorf("create unoccupied destination %s: %w", path, err))
			}
			s.CreatedPaths = append(s.CreatedPaths, path)
			if err = m.save(s); err != nil {
				return err
			}
		}
		s.ViewsCreated = true
		s.Phase = "launching"
		if err = m.save(s); err != nil {
			return err
		}
		disks := make([]appliance.Disk, 0, len(s.Disks))
		for _, disk := range s.Disks {
			disks = append(disks, appliance.Disk{Path: disk.Path, Format: disk.Format})
		}
		h, err = appliance.Open(disks, s.ReadOnly)
		if err != nil {
			return m.fail(s, err)
		}
		pid, err := h.PID()
		if err != nil {
			return m.fail(s, err)
		}
		s.Appliance, err = identity(pid)
		if err != nil {
			return m.fail(s, err)
		}
		s.Phase = "mounting"
		if err = m.save(s); err != nil {
			return err
		}
		s.Inventory, err = h.Prepare()
		if err != nil {
			return m.fail(s, err)
		}
		s.Phase = "prepared"
		if err = m.save(s); err != nil {
			return err
		}
		if err = h.Export(s.Staging, s.Token); err != nil {
			return m.fail(s, err)
		}
		exported = true
		go func() { runDone <- h.Run() }()
		if err = bindView(filepath.Join(s.Staging, "root"), s.Public, s.ReadOnly); err != nil {
			return m.fail(s, err)
		}
		if err = bindView(filepath.Join(s.Staging, "extras"), s.Extras, s.ReadOnly); err != nil {
			return m.fail(s, err)
		}
		listener, err = net.Listen("unix", m.socketPath(id))
		if err != nil {
			return m.fail(s, err)
		}
		// Persistent cleanup owns this path; a late listener close must not unlink
		// the socket of a later session after the operation lock is released.
		listener.(*net.UnixListener).SetUnlinkOnClose(false)
		if err = os.Chmod(m.socketPath(id), 0600); err != nil {
			return m.fail(s, err)
		}
		s.Phase = "mounted"
		return m.save(s)
	})
	if err != nil {
		if h != nil && !exported {
			if closeErr := h.Close(); closeErr != nil {
				return errors.Join(err, closeErr)
			}
		}
		return err
	}
	defer listener.Close()
	slog.Info("repair mounted", "vmid", id, "read_only", s.ReadOnly)
	for {
		conn, err := listener.Accept()
		if err != nil {
			return err
		}
		_ = conn.SetDeadline(time.Now().Add(5 * time.Minute))
		line, err := bufio.NewReader(conn).ReadString('\n')
		if err != nil || strings.TrimSpace(line) != s.Token {
			fmt.Fprintln(conn, "invalid repair-session ownership")
			conn.Close()
			continue
		}
		err = m.locked(func() error { return m.finalize(ctx, s, h, runDone, unmountOwned) })
		if err != nil {
			fmt.Fprintln(conn, err.Error())
			conn.Close()
			continue
		}
		fmt.Fprintln(conn, "ok")
		conn.Close()
		return nil
	}
}

func bindView(source, target string, ro bool) error {
	if err := exec.Command("mount", "--bind", source, target).Run(); err != nil {
		return fmt.Errorf("bind public view: %w", err)
	}
	if ro {
		if err := exec.Command("mount", "-o", "remount,bind,ro", target).Run(); err != nil {
			return fmt.Errorf("enforce read-only public view: %w", err)
		}
	}
	return nil
}

func unmount(path string) error {
	mounted, err := mountedAt(path)
	if err != nil {
		return err
	}
	if !mounted {
		return nil
	}
	if output, err := exec.Command("umount", path).CombinedOutput(); err != nil {
		return fmt.Errorf("your mount %s is busy or could not be unmounted; protection is retained: %w\n%s\n%s", path, err, diagnosticOutput(output, 1024), busyMountDiagnostic(path))
	}
	return nil
}

func (m *Manager) finalize(ctx context.Context, s *State, h interface{ Close() error }, runDone <-chan error, unmountView func(*State, string) error) error {
	s.Phase = "finalizing"
	if err := m.save(s); err != nil {
		return err
	}
	for _, path := range []string{s.Extras, s.Public} {
		if err := unmountView(s, path); err != nil {
			return m.fail(s, err)
		}
	}
	if err := unmountView(s, s.Staging); err != nil {
		return m.fail(s, err)
	}
	if !s.FuseStopped {
		select {
		case err := <-runDone:
			if err != nil {
				return m.fail(s, fmt.Errorf("FUSE finalization: %w", err))
			}
			s.FuseStopped = true
			if err := m.save(s); err != nil {
				return err
			}
		case <-time.After(30 * time.Second):
			return m.fail(s, errors.New("your FUSE worker has not exited; protection is retained"))
		}
	}
	if err := h.Close(); err != nil {
		return m.fail(s, fmt.Errorf("appliance finalization: %w", err))
	}
	if err := assertNoMounts(s); err != nil {
		return m.fail(s, err)
	}
	if err := assertDisksUnused(s.Disks); err != nil {
		return m.fail(s, err)
	}
	return m.finish(s, func() error { return proxmox.New().Release(ctx, s.VMID, s.Token) })
}

func decodeMount(value string) string {
	r := strings.NewReplacer("\\040", " ", "\\011", "\t", "\\012", "\n", "\\134", "\\")
	return r.Replace(value)
}

func mountPoints() ([]string, error) {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 6 {
			return nil, errors.New("invalid mount information")
		}
		paths = append(paths, decodeMount(fields[4]))
	}
	return paths, nil
}

func mountedAt(path string) (bool, error) {
	points, err := mountPoints()
	if err != nil {
		return false, err
	}
	for _, point := range points {
		if point == path {
			return true, nil
		}
	}
	return false, nil
}

func assertNoMounts(s *State) error {
	points, err := mountPoints()
	if err != nil {
		return err
	}
	for _, point := range points {
		for _, path := range []string{s.Staging, s.Public, s.Extras} {
			if path != "" && (point == path || strings.HasPrefix(point, path+"/")) {
				return fmt.Errorf("your mount %s still exists; protection is retained", point)
			}
		}
	}
	return nil
}

func assertDisksUnused(disks []proxmox.Disk) error {
	if len(disks) == 0 {
		return nil
	}
	if err := kernelDisksUnused(disks); err != nil {
		return err
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		fds, err := os.ReadDir(filepath.Join("/proc", entry.Name(), "fd"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("cannot reconcile process disk ownership: %w", err)
		}
		for _, fd := range fds {
			path := filepath.Join("/proc", entry.Name(), "fd", fd.Name())
			fi, err := os.Stat(path)
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESTALE) {
				continue
			}
			if err != nil {
				return fmt.Errorf("cannot reconcile an open descriptor: %w", err)
			}
			for _, disk := range disks {
				di, err := os.Stat(disk.Path)
				if err != nil {
					return err
				}
				if sameDisk(fi, di) {
					return diskUserError(disk.Path, "/proc", entry.Name(), fd.Name())
				}
			}
		}
	}
	return nil
}

func sameDisk(a, b os.FileInfo) bool {
	if os.SameFile(a, b) {
		return true
	}
	as, aok := a.Sys().(*syscall.Stat_t)
	bs, bok := b.Sys().(*syscall.Stat_t)
	return aok && bok && a.Mode()&os.ModeDevice != 0 && b.Mode()&os.ModeDevice != 0 && as.Rdev == bs.Rdev
}

func recoverMounts(s *State) error {
	for _, path := range []string{s.Extras, s.Public, s.Staging} {
		if path == "" {
			continue
		}
		if err := unmountOwned(s, path); err != nil {
			return err
		}
	}
	return assertNoMounts(s)
}

func unmountOwned(s *State, path string) error {
	if err := checkMountOwnership(s, path); err != nil {
		return err
	}
	return unmount(path)
}

func checkMountOwnership(s *State, path string) error {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return err
	}
	return checkMountInfo(s, path, string(data))
}

func checkMountInfo(s *State, path, data string) error {
	for _, line := range strings.Split(data, "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 6 {
			return errors.New("invalid mount information")
		}
		target := decodeMount(fields[4])
		if target != path && !strings.HasPrefix(target, path+"/") {
			continue
		}
		if target != path {
			return fmt.Errorf("unrecorded nested mount %s prevents cleanup; inspect with: findmnt --mountpoint %s", target, quoteDiagnosticArgument(target))
		}
		sep := strings.Index(line, " - ")
		if sep < 0 {
			return errors.New("invalid mount information")
		}
		tail := strings.Fields(line[sep+3:])
		if len(tail) < 2 {
			return errors.New("invalid mount filesystem information")
		}
		if !strings.HasPrefix(tail[0], "fuse") || decodeMount(tail[1]) != "pvedisk-"+s.Token {
			return fmt.Errorf("mount at %s is not a recognizable libguestfs session; protection retained. Inspect its ownership with: findmnt --mountpoint %s", path, quoteDiagnosticArgument(path))
		}
	}
	return nil
}
