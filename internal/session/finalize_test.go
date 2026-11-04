package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

func mountInfoLine(target, kind, source string) string {
	return fmt.Sprintf("42 1 0:99 / %s rw,nosuid - %s %s rw\n", target, kind, source)
}

func TestMountOwnership(t *testing.T) {
	s := &State{Token: strings.Repeat("a", 64)}
	path := "/repair/vm400"
	own := mountInfoLine(path, "fuse.guestfs", "pvedisk-"+s.Token)
	for _, tc := range []struct {
		name, data string
		wantErr    bool
	}{
		{"owned", own, false},
		{"absent", mountInfoLine("/unrelated", "ext4", "/dev/sda1"), false},
		{"other session", mountInfoLine(path, "fuse.guestfs", "pvedisk-another-token"), true},
		{"different filesystem", mountInfoLine(path, "ext4", "pvedisk-"+s.Token), true},
		{"nested mount", own + mountInfoLine(path+"/home", "fuse.guestfs", "pvedisk-"+s.Token), true},
		{"truncated mount information", "bad", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkMountInfo(s, path, tc.data)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestMountOwnershipEscapedPath(t *testing.T) {
	s := &State{Token: strings.Repeat("a", 64)}
	data := mountInfoLine("/repair\\040space/vm400", "fuse.guestfs", "pvedisk-"+s.Token)
	if err := checkMountInfo(s, "/repair space/vm400", data); err != nil {
		t.Fatal(err)
	}
	if err := checkMountInfo(&State{Token: "other"}, "/repair space/vm400", data); err == nil {
		t.Fatal("escaped path bypassed ownership")
	}
}

type closeFailure struct {
	calls int
	err   error
}

func (h *closeFailure) Close() error { h.calls++; return h.err }

func finalizeState(t *testing.T) (*Manager, *State) {
	t.Helper()
	m := testManager(t)
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := m.intent(400, root, false)
	if err != nil {
		t.Fatal(err)
	}
	s.ProtectionAttempted = true
	return m, s
}

func TestBusyUnmountRetainsSessionBeforeApplianceFinalization(t *testing.T) {
	m, s := finalizeState(t)
	h := &closeFailure{}
	err := m.finalize(context.Background(), s, h, nil, func(*State, string) error { return syscall.EBUSY })
	if !errors.Is(err, syscall.EBUSY) {
		t.Fatalf("busy error=%v", err)
	}
	if h.calls != 0 {
		t.Fatal("appliance closed while public view busy")
	}
	stored, err := m.Status(400)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Phase != "failed" || !stored.ProtectionAttempted {
		t.Fatal("busy session lost its protected state")
	}
}

func TestUnknownMountRetainsSession(t *testing.T) {
	m, s := finalizeState(t)
	h := &closeFailure{}
	err := m.finalize(context.Background(), s, h, nil, func(s *State, path string) error {
		return checkMountInfo(s, path, mountInfoLine(path, "fuse.guestfs", "pvedisk-another-token"))
	})
	if err == nil {
		t.Fatal("conflicting mount accepted")
	}
	if h.calls != 0 {
		t.Fatal("appliance finalized despite uncertain mount ownership")
	}
	if _, err = m.Status(400); err != nil {
		t.Fatal("uncertain session state removed")
	}
}

func TestFinalizationRetryDoesNotWaitForConsumedFUSEEvent(t *testing.T) {
	m, s := finalizeState(t)
	closeErr := errors.New("appliance shutdown failed")
	h := &closeFailure{err: closeErr}
	done := make(chan error, 1)
	done <- nil
	unmounted := func(*State, string) error { return nil }
	if err := m.finalize(context.Background(), s, h, done, unmounted); !errors.Is(err, closeErr) {
		t.Fatalf("first finalization error=%v", err)
	}
	stored, err := m.Status(400)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.FuseStopped {
		t.Fatal("completed FUSE teardown not persisted")
	}
	start := time.Now()
	if err = m.finalize(context.Background(), stored, h, done, unmounted); !errors.Is(err, closeErr) {
		t.Fatalf("retry error=%v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("retry waited for an already consumed FUSE event")
	}
	if h.calls != 2 {
		t.Fatalf("appliance close attempts=%d", h.calls)
	}
}
