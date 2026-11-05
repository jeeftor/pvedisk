package session

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func testManager(t *testing.T) *Manager {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	return &Manager{root: root}
}

func TestIntentConflictAndDestinationChange(t *testing.T) {
	m := testManager(t)
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := m.locked(func() error { _, err := m.intent(400, root, false); return err }); err != nil {
		t.Fatal(err)
	}
	active, err := m.HasSessions()
	if err != nil || !active {
		t.Fatalf("active=%v err=%v", active, err)
	}
	if _, err := m.intent(400, root, false); !errors.Is(err, ErrSessionExists) {
		t.Fatalf("conflicting session did not report its recoverable cause: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "vm400")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("intent unexpectedly changed public destination")
	}
}

func TestOccupiedDestinationUntouched(t *testing.T) {
	m := testManager(t)
	root := t.TempDir()
	_ = os.Chmod(root, 0700)
	path := filepath.Join(root, "vm400")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := m.intent(400, root, false); err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "already exists") || !strings.Contains(err.Error(), "findmnt") {
		t.Fatalf("expected an actionable conflict error, got %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("occupied directory changed")
	}
}

func TestIntentInspectionFailureDoesNotOfferRecovery(t *testing.T) {
	m := &Manager{root: filepath.Join(t.TempDir(), "not-a-directory")}
	if err := os.WriteFile(m.root, []byte("unrelated data"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.intent(400, t.TempDir(), false); err == nil || errors.Is(err, ErrSessionExists) || !strings.Contains(err.Error(), "inspect your repair session") {
		t.Fatalf("inspection failure misreported as a recoverable session: %v", err)
	}
}

func TestCorruptStateBlocksSetup(t *testing.T) {
	m := testManager(t)
	if err := os.WriteFile(m.statePath(400), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	active, err := m.HasSessions()
	if err != nil || !active {
		t.Fatal("corrupt state must block destination changes")
	}
	if _, err := m.Status(400); err == nil {
		t.Fatal("corrupt state accepted")
	}
}

func TestRecordedPathsRejectTampering(t *testing.T) {
	m := testManager(t)
	root := t.TempDir()
	_ = os.Chmod(root, 0700)
	s, err := m.intent(400, root, false)
	if err != nil {
		t.Fatal(err)
	}
	s.Public = "/etc"
	if err = m.save(s); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Status(400); err == nil {
		t.Fatal("corrupt public path accepted")
	}
}

func TestCleanupDoesNotRemoveUnownedPaths(t *testing.T) {
	m := testManager(t)
	root := t.TempDir()
	_ = os.Chmod(root, 0700)
	s, err := m.intent(400, root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(s.Public, 0700); err != nil {
		t.Fatal(err)
	}
	if err = m.finish(s, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(s.Public); err != nil {
		t.Fatal("unrecorded conflicting path removed")
	}
}

func TestPasswordStateIsPrivateAndScrubbed(t *testing.T) {
	m := testManager(t)
	s := &State{VMID: 400, Token: strings.Repeat("a", 64), Phase: "password", PasswordFile: filepath.Join(m.root, ".password-"+strings.Repeat("a", 64))}
	if err := m.save(s); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.PasswordFile, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := scrubPassword(s.PasswordFile, m.root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.PasswordFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("password remains")
	}
	data, err := os.ReadFile(m.statePath(400))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret") {
		t.Fatal("password leaked into state")
	}
	fi, err := os.Stat(m.statePath(400))
	if err != nil || fi.Mode().Perm() != 0600 {
		t.Fatal("state not private")
	}
}

func TestInvalidPasswordIsCleared(t *testing.T) {
	m := testManager(t)
	password := []byte("bad\npassword")
	if err := m.ResetRoot(context.Background(), 400, "/missing", password); err == nil {
		t.Fatal("newline accepted")
	}
	for _, b := range password {
		if b != 0 {
			t.Fatal("password not cleared")
		}
	}
}

func TestOperationLockNotInheritedByChild(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux descriptor inheritance")
	}
	if os.Getenv("PVEDISK_TEST_LOCK_CHILD") == "1" {
		fds, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Fatal(err)
		}
		for _, fd := range fds {
			target, _ := os.Readlink(filepath.Join("/proc/self/fd", fd.Name()))
			if strings.HasSuffix(target, "/operations.lock") {
				t.Fatal("operation descriptor survived exec")
			}
		}
		return
	}
	m := testManager(t)
	if err := m.locked(func() error {
		child := exec.Command(os.Args[0], "-test.run=^TestOperationLockNotInheritedByChild$")
		child.Env = append(os.Environ(), "PVEDISK_TEST_LOCK_CHILD=1")
		data, err := child.CombinedOutput()
		if err != nil {
			t.Fatalf("child inherited operation descriptor: %s", data)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIdentityDetectsPIDReuse(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux process identity")
	}
	i, err := identity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	live, err := alive(i)
	if err != nil || !live {
		t.Fatalf("own identity=%v err=%v", live, err)
	}
	i.Start = "wrong"
	live, err = alive(i)
	if err != nil || live {
		t.Fatal("reused PID identity accepted")
	}
}

func TestRecoverPreAcquireIntent(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux recovery mount reconciliation")
	}
	m := testManager(t)
	root := t.TempDir()
	_ = os.Chmod(root, 0700)
	if _, err := m.intent(400, root, false); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.Recover(ctx, 400); err != nil {
		t.Fatal(err)
	}
	active, err := m.HasSessions()
	if err != nil || active {
		t.Fatal("pre-acquire recovery left state")
	}
}

func TestStateRoundTrip(t *testing.T) {
	m := testManager(t)
	root := t.TempDir()
	_ = os.Chmod(root, 0700)
	s, err := m.intent(400, root, true)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := m.Status(400)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(s)
	b, _ := json.Marshal(loaded)
	if string(a) != string(b) {
		t.Fatal("state roundtrip differs")
	}
}
