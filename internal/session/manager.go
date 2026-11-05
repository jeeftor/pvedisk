package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jeeftor/pvedisk/internal/appliance"
	"github.com/jeeftor/pvedisk/internal/config"
	"github.com/jeeftor/pvedisk/internal/proxmox"
)

// ErrSessionExists means a recorded repair session must be cleaned up before mounting.
var ErrSessionExists = errors.New("a repair session already exists; inspect status and use unmount or recover")

func (m *Manager) intent(id int, root string, ro bool) (*State, error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(m.statePath(id)); err == nil {
		return nil, ErrSessionExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect your repair session: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(root) || canonical == "/" {
		return nil, errors.New("your mount root must be an absolute directory other than /")
	}
	fi, err := os.Stat(canonical)
	if err != nil || !fi.IsDir() {
		return nil, errors.New("your mount root must be an existing directory")
	}
	if fi.Mode().Perm()&0022 != 0 {
		return nil, errors.New("your mount root must not be writable by your group or other users")
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Geteuid() {
		return nil, errors.New("your mount root must be owned by root")
	}
	token := make([]byte, 32)
	if _, err = rand.Read(token); err != nil {
		return nil, err
	}
	s := &State{VMID: id, Token: hex.EncodeToString(token), Phase: "pending", Root: canonical, ReadOnly: ro, Public: filepath.Join(canonical, fmt.Sprintf("vm%d", id)), Extras: filepath.Join(canonical, fmt.Sprintf("vm%d-extra", id)), Staging: filepath.Join(m.root, fmt.Sprintf("vm%d-stage", id))}
	for _, p := range []string{s.Public, s.Extras, s.Staging} {
		if _, err := os.Lstat(p); err == nil {
			return nil, fmt.Errorf("your mount destination %q already exists; it may contain files or mounts from an earlier tool or session. Inspect it with findmnt and check pvedisk status %d before moving or removing it; this attempt has not requested a guest shutdown", p, id)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect your mount destination %q: %w", p, err)
		}
	}
	return s, m.save(s)
}

// Mount starts a detached worker and waits until its public views are available.
func (m *Manager) Mount(ctx context.Context, id int, configPath string, ro bool, progress func(string)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if runtime.GOOS != "linux" {
		return errors.New("repair operations require Linux on a Proxmox host")
	}
	err := m.lockedContext(ctx, func() error {
		cfg, err := config.Load(configPath)
		if err != nil {
			return err
		}
		s, err := m.intent(id, cfg.MountRoot, ro)
		if err != nil {
			return err
		}
		if progress != nil {
			progress(s.Phase)
		}
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		log, err := os.OpenFile(filepath.Join(m.root, strconv.Itoa(id)+".log"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		defer log.Close()
		cmd := exec.Command(exe, "_worker", strconv.Itoa(id))
		cmd.Stdin = nil
		cmd.Stdout = log
		cmd.Stderr = log
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err = cmd.Start(); err != nil {
			s.Phase = "failed"
			s.Error = err.Error()
			_ = m.save(s)
			return err
		}
		_ = cmd.Process.Release()
		return nil
	})
	if err != nil {
		return err
	}
	timer := time.NewTicker(200 * time.Millisecond)
	defer timer.Stop()
	deadline := time.NewTimer(5 * time.Minute)
	defer deadline.Stop()
	lastPhase := "pending"
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("your worker continues independently; inspect status: %w", ctx.Err())
		case <-deadline.C:
			return errors.New("worker startup has not completed; inspect status and the private session log")
		case <-timer.C:
			s, err := m.Status(id)
			if err != nil {
				return err
			}
			if s.Phase != lastPhase {
				lastPhase = s.Phase
				if progress != nil {
					progress(s.Phase)
				}
			}
			if s.Phase == "mounted" {
				return nil
			}
			if s.Phase == "failed" {
				return fmt.Errorf("repair worker: %s", s.Error)
			}
		}
	}
}

// Unmount asks the recorded worker to finalize its appliance before unlocking.
func (m *Manager) Unmount(ctx context.Context, id int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validID(id); err != nil {
		return err
	}
	s, err := m.Status(id)
	if err != nil {
		return err
	}
	live, err := alive(s.Worker)
	if err != nil {
		return err
	}
	if !live {
		return fmt.Errorf("your recorded repair worker for VM %d is not running (recorded PID %d); run: pvedisk recover %d", id, s.Worker.PID, id)
	}
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "unix", m.socketPath(id))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("cannot contact your repair worker for VM %d (PID %d) at %s: %w; inspect pvedisk status %d and the private worker log %s before retrying pvedisk unmount %d", id, s.Worker.PID, m.socketPath(id), err, id, filepath.Join(m.root, strconv.Itoa(id)+".log"), id)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(5 * time.Minute))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err = fmt.Fprintln(conn, s.Token); err != nil {
		return fmt.Errorf("cannot confirm your cleanup request for VM %d reached its worker: %w; inspect pvedisk status %d before retrying", id, err, id)
	}
	buf := make([]byte, 8192)
	n, err := conn.Read(buf)
	if err != nil {
		return fmt.Errorf("your worker response for VM %d ended before cleanup was confirmed: %w; inspect pvedisk status %d before retrying unmount, or use pvedisk recover %d if the worker has exited", id, err, id, id)
	}
	response := strings.TrimSpace(string(buf[:n]))
	if response != "ok" {
		return fmt.Errorf("%s\nAfter resolving the blocker, retry: pvedisk unmount %d", response, id)
	}
	return nil
}

// Recover reconciles recorded processes, mounts and disk users before unlocking.
func (m *Manager) Recover(ctx context.Context, id int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s, err := m.Status(id)
	if err != nil {
		return err
	}
	live, err := alive(s.Worker)
	if err != nil {
		return err
	}
	if live {
		return m.Unmount(ctx, id)
	}
	err = m.lockedContext(ctx, func() error {
		s, err := m.Status(id)
		if err != nil {
			return err
		}
		if live, err := alive(s.Worker); err != nil || live {
			if err != nil {
				return err
			}
			return fmt.Errorf("your repair worker for VM %d is active (PID %d); run: pvedisk unmount %d", id, s.Worker.PID, id)
		}
		if live, err := alive(s.Appliance); err != nil || live {
			if err != nil {
				return err
			}
			return fmt.Errorf("your recorded libguestfs appliance for VM %d is still running in process %s; protection is retained. Inspect with: ps -p %d -o pid=,comm=", id, processDescription("/proc", strconv.Itoa(s.Appliance.PID)), s.Appliance.PID)
		}
		if err := recoverMounts(s); err != nil {
			return err
		}
		if err := assertRecoveryDisksUnused(s); err != nil {
			return err
		}
		if s.PasswordFile != "" {
			if err := scrubPassword(s.PasswordFile, m.root); err != nil {
				return err
			}
		}
		return m.finish(s, func() error {
			if !s.ProtectionAttempted {
				return nil
			}
			return proxmox.New().Release(ctx, id, s.Token)
		})
	})
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("cannot recover your recorded session for VM %d: %w\nInspect the saved session with: pvedisk status %d\nAfter resolving the blocker, retry: pvedisk recover %d", id, err, id, id)
	}
	return err
}

// ResetRoot changes the root password while holding the same startup protection.
func (m *Manager) ResetRoot(ctx context.Context, id int, configPath string, password []byte) error {
	defer clear(password)
	if len(password) == 0 || strings.ContainsAny(string(password), "\x00\r\n") {
		return errors.New("your password must be nonempty and contain no newline or zero byte")
	}
	return m.offlineMaintenance(ctx, id, configPath, "password", func(s *State) error {
		if len(s.Disks) > 1 {
			disks := make([]appliance.Disk, 0, len(s.Disks))
			for _, disk := range s.Disks {
				disks = append(disks, appliance.Disk{Path: disk.Path, Format: disk.Format})
			}
			if err := appliance.CheckCustomizationMounts(disks); err != nil {
				return fmt.Errorf("verify your guest disk ordering before password repair: %w", err)
			}
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		return m.resetRootOffline(ctx, s, password)
	})
}

func (m *Manager) fail(s *State, err error) error {
	s.Phase = "failed"
	s.Error = err.Error()
	if saveErr := m.save(s); saveErr != nil {
		return errors.Join(err, saveErr)
	}
	return err
}

// finish removes only owned empty mount directories before releasing protection.
// State remains until every per-session artifact has been removed successfully.
func (m *Manager) finish(s *State, release func() error) error {
	if err := m.checkArtifacts(s); err != nil {
		return m.fail(s, err)
	}
	if err := m.removeDirectories(s); err != nil {
		return m.fail(s, err)
	}
	s.Phase = "finalized"
	s.Error = ""
	if err := m.save(s); err != nil {
		return err
	}
	if err := release(); err != nil {
		return m.fail(s, fmt.Errorf("protection retained: %w", err))
	}
	if err := m.removeState(s); err != nil {
		return m.fail(s, fmt.Errorf("protection released but session cleanup is incomplete; use recover: %w", err))
	}
	return nil
}

func (m *Manager) removeDirectories(s *State) error {
	if err := m.validatePaths(s); err != nil {
		return err
	}
	// Check every destination before removing any. Guest data and unexpected host
	// files must never become cleanup targets, even after a successful unmount.
	for _, p := range s.CreatedPaths {
		fi, err := os.Lstat(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect your session directory %q: %w", p, err)
		}
		if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("your session path %q is no longer a directory; protection is retained", p)
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			return fmt.Errorf("inspect your session directory %q: %w", p, err)
		}
		if len(entries) != 0 {
			return fmt.Errorf("your session directory %q contains unexpected files or directories; protection is retained", p)
		}
	}
	for _, p := range s.CreatedPaths {
		if p != "" {
			if err := syscall.Rmdir(p); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove your empty session directory %q; protection is retained: %w", p, err)
			}
			if err := syncDirectory(filepath.Dir(p)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *Manager) removeState(s *State) error {
	if err := m.checkArtifacts(s); err != nil {
		return err
	}
	for _, path := range []string{m.socketPath(s.VMID), filepath.Join(m.root, strconv.Itoa(s.VMID)+".log")} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := syncDirectory(m.root); err != nil {
		return err
	}
	if err := os.Remove(m.statePath(s.VMID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDirectory(m.root)
}

func (m *Manager) checkArtifacts(s *State) error {
	for _, path := range []string{m.socketPath(s.VMID), filepath.Join(m.root, strconv.Itoa(s.VMID)+".log")} {
		fi, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if path == m.socketPath(s.VMID) && fi.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("unexpected data at your session socket %q", path)
		}
		if path != m.socketPath(s.VMID) && !fi.Mode().IsRegular() {
			return fmt.Errorf("unexpected data at your session log %q", path)
		}
	}
	return nil
}

func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func scrubPassword(path, root string) error {
	if filepath.Dir(path) != root || !strings.HasPrefix(filepath.Base(path), ".password-") {
		return errors.New("invalid recorded password path")
	}
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return errors.New("unsafe password file")
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, err = f.Write(make([]byte, fi.Size()))
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Remove(path)
}
