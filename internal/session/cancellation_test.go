package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

func holdOperationLock(t *testing.T, m *Manager) func() {
	t.Helper()
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan error, 1)
	go func() { finished <- m.locked(func() error { close(entered); <-release; return nil }) }()
	select {
	case <-entered:
	case err := <-finished:
		t.Fatalf("hold operation lock: %v", err)
	case <-time.After(time.Second):
		t.Fatal("could not acquire operation lock")
	}
	var once sync.Once
	unlock := func() {
		once.Do(func() {
			close(release)
			select {
			case err := <-finished:
				if err != nil {
					t.Errorf("release operation lock: %v", err)
				}
			case <-time.After(time.Second):
				t.Error("operation lock was not released")
			}
		})
	}
	t.Cleanup(unlock)
	return unlock
}

func TestOperationLockWaitHonorsCancellation(t *testing.T) {
	m := testManager(t)
	unlock := holdOperationLock(t, m)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	called := false
	start := time.Now()
	err := m.lockedContext(ctx, func() error { called = true; return nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("canceled lock wait=%v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("lock wait ignored cancellation")
	}
	if called {
		t.Fatal("canceled callback ran while lock held")
	}
	unlock()
	if called {
		t.Fatal("canceled callback ran after lock release")
	}
	if err := m.locked(func() error { return nil }); err != nil {
		t.Fatalf("lock unusable after cancellation: %v", err)
	}
}

func TestOperationLockStillSerializes(t *testing.T) {
	m := testManager(t)
	unlock := holdOperationLock(t, m)
	called := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- m.lockedContext(context.Background(), func() error { close(called); return nil }) }()
	select {
	case <-called:
		t.Fatal("callback ran while another operation held lock")
	case <-time.After(100 * time.Millisecond):
	}
	unlock()
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("callback did not run after release")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestCanceledOperationLockDoesNotCreateState(t *testing.T) {
	m := &Manager{root: filepath.Join(t.TempDir(), "missing")}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.lockedContext(ctx, func() error { t.Fatal("canceled callback ran"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled operation created state directory")
	}
}

func TestQueuedMountCancellationDoesNotStartWorker(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux repair operations")
	}
	m := testManager(t)
	unlock := holdOperationLock(t, m)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	progress := false
	err := m.Mount(ctx, 400, filepath.Join(m.root, "missing-config.yaml"), false, func(string) { progress = true })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("canceled queued mount loaded config or started worker: %v", err)
	}
	unlock()
	if progress {
		t.Fatal("queued mount started after cancellation")
	}
	entries, err := os.ReadDir(m.root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "operations.lock" {
		t.Fatalf("queued mount created session artifacts: %+v", entries)
	}
}

func TestQueuedCleanupCancellationPreservesSession(t *testing.T) {
	m := testManager(t)
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
	before, err := os.ReadFile(m.statePath(400))
	if err != nil {
		t.Fatal(err)
	}
	unlock := holdOperationLock(t, m)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := m.Clean(ctx, 400); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("canceled queued cleanup=%v", err)
	}
	unlock()
	after, err := os.ReadFile(m.statePath(400))
	if err != nil || string(before) != string(after) {
		t.Fatal("canceled cleanup changed state")
	}
	for _, path := range s.CreatedPaths {
		if _, err := os.Stat(path); err != nil {
			t.Fatal("canceled cleanup removed session directory")
		}
	}
}

func TestCanceledUnmountDoesNotRequestCleanup(t *testing.T) {
	m := &Manager{root: filepath.Join(t.TempDir(), "missing")}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.Unmount(ctx, 400); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled unmount inspected or contacted worker: %v", err)
	}
	if _, err := os.Stat(m.root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled unmount created state")
	}
}
