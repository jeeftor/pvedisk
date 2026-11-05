package session

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func cleanupSession(t *testing.T) (*Manager, *State) {
	t.Helper()
	dir, err := os.MkdirTemp("/private/tmp", "pvedisk-cleanup-")
	if err != nil {
		dir, err = os.MkdirTemp("/tmp", "pvedisk-cleanup-")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Errorf("unexpected session test debris: %v", err)
		}
	})
	m := &Manager{root: dir}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := m.intent(400, root, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{s.Public, s.Extras, s.Staging} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		s.CreatedPaths = append(s.CreatedPaths, path)
	}
	if err := m.save(s); err != nil {
		t.Fatal(err)
	}
	return m, s
}

func TestSuccessfulCleanupRemovesSessionStructure(t *testing.T) {
	m, s := cleanupSession(t)
	log := filepath.Join(m.root, "400.log")
	if err := os.WriteFile(log, []byte("diagnostic log"), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", m.socketPath(400))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	unrelated := filepath.Join(s.Root, "keep.txt")
	if err := os.WriteFile(unrelated, []byte("host data"), 0600); err != nil {
		t.Fatal(err)
	}
	guestImage := filepath.Join(s.Root, "guest.img")
	if err := os.WriteFile(guestImage, []byte("guest data"), 0600); err != nil {
		t.Fatal(err)
	}
	released := 0
	err = m.finish(s, func() error {
		released++
		for _, path := range s.CreatedPaths {
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("release before directory cleanup: %s", path)
			}
		}
		if _, err := m.Status(400); err != nil {
			t.Fatal("session state removed before protection release")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if released != 1 {
		t.Fatalf("release calls=%d", released)
	}
	for _, path := range append(append([]string{}, s.CreatedPaths...), m.statePath(400), m.socketPath(400), log) {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("session artifact remains: %s (%v)", path, err)
		}
	}
	if fi, err := os.Stat(s.Root); err != nil || !fi.IsDir() {
		t.Fatal("configured mount root removed")
	}
	for path, want := range map[string]string{unrelated: "host data", guestImage: "guest data"} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != want {
			t.Fatalf("unrelated data changed at %s", path)
		}
	}
	active, err := m.HasSessions()
	if err != nil || active {
		t.Fatal("successful cleanup still blocks setup")
	}
}

func TestCleanupUnexpectedDataRetainsProtection(t *testing.T) {
	for _, kind := range []string{"file", "directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			m, s := cleanupSession(t)
			path := filepath.Join(s.Extras, "unexpected")
			switch kind {
			case "file":
				if err := os.WriteFile(path, []byte("do not delete"), 0600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(s.Root, path); err != nil {
					t.Fatal(err)
				}
			}
			released := false
			err := m.finish(s, func() error { released = true; return nil })
			if err == nil {
				t.Fatal("unexpected data accepted")
			}
			if released {
				t.Fatal("protection released before failed cleanup")
			}
			for _, dir := range s.CreatedPaths {
				if _, err := os.Stat(dir); err != nil {
					t.Fatal("cleanup partially deleted directories despite failed preflight")
				}
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatal("unexpected data deleted")
			}
			if _, err := m.Status(400); err != nil {
				t.Fatal("failed cleanup lost cleanup state")
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := m.finish(s, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCleanupRejectsReplacedDirectory(t *testing.T) {
	m, s := cleanupSession(t)
	if err := os.Remove(s.Public); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Public, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	released := false
	if err := m.finish(s, func() error { released = true; return nil }); err == nil {
		t.Fatal("replaced directory accepted")
	}
	if released {
		t.Fatal("protection released")
	}
	if data, err := os.ReadFile(s.Public); err != nil || string(data) != "replacement" {
		t.Fatal("replacement file modified")
	}
	if err := os.Remove(s.Public); err != nil {
		t.Fatal(err)
	}
	if err := m.finish(s, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupRetryAfterReleaseFailure(t *testing.T) {
	m, s := cleanupSession(t)
	releaseErr := errors.New("release failed")
	if err := m.finish(s, func() error { return releaseErr }); !errors.Is(err, releaseErr) {
		t.Fatalf("release error=%v", err)
	}
	for _, dir := range s.CreatedPaths {
		if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("empty session directory still exists")
		}
	}
	stored, err := m.Status(400)
	if err != nil || stored.Phase != "failed" {
		t.Fatal("failed release lost session state")
	}
	if err := m.finish(stored, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := m.finish(stored, func() error { return nil }); err != nil {
		t.Fatalf("cleanup is not idempotent: %v", err)
	}
}

func TestCleanupUnexpectedArtifactRetainsProtection(t *testing.T) {
	m, s := cleanupSession(t)
	path := m.socketPath(400)
	if err := os.WriteFile(path, []byte("unrelated"), 0600); err != nil {
		t.Fatal(err)
	}
	released := false
	err := m.finish(s, func() error { released = true; return nil })
	if err == nil || !strings.Contains(err.Error(), "unexpected data") {
		t.Fatalf("unexpected artifact error=%v", err)
	}
	if released {
		t.Fatal("unexpected artifact released protection")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "unrelated" {
		t.Fatal("unrelated artifact changed")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := m.finish(s, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}
