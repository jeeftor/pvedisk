package session

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func diagnosticSession(t *testing.T) (*Manager, *State) {
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
	return m, s
}

func TestUnmountMissingWorkerShowsCleanupCommand(t *testing.T) {
	m, s := diagnosticSession(t)
	before, err := os.ReadFile(m.statePath(400))
	if err != nil {
		t.Fatal(err)
	}
	err = m.Unmount(context.Background(), 400)
	if err == nil {
		t.Fatal("missing worker accepted")
	}
	for _, want := range []string{"VM 400", "recorded PID 0", "pvedisk clean 400"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("missing stale-worker diagnostic %q: %v", want, err)
		}
	}
	after, err := os.ReadFile(m.statePath(s.VMID))
	if err != nil || string(before) != string(after) {
		t.Fatal("diagnostic changed saved session")
	}
}

func TestUnmountUnreachableWorkerShowsPIDSocketAndLog(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux process identities")
	}
	m, s := diagnosticSession(t)
	var err error
	s.Worker, err = identity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.save(s); err != nil {
		t.Fatal(err)
	}
	err = m.Unmount(context.Background(), 400)
	if err == nil {
		t.Fatal("missing socket accepted")
	}
	for _, want := range []string{strconv.Itoa(os.Getpid()), m.socketPath(400), filepath.Join(m.root, "400.log"), "pvedisk status 400", "pvedisk unmount 400"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("missing worker inspection detail %q: %v", want, err)
		}
	}
}

func TestCleanupLiveApplianceIdentifiesProcessAndKeepsState(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux process identities")
	}
	m, s := diagnosticSession(t)
	var err error
	s.Appliance, err = identity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.save(s); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(m.statePath(400))
	if err != nil {
		t.Fatal(err)
	}
	err = m.Clean(context.Background(), 400)
	if err == nil {
		t.Fatal("active appliance accepted")
	}
	for _, want := range []string{"VM 400", "libguestfs appliance", fmt.Sprintf("ps -p %d -o pid=,comm=", os.Getpid()), "protection is retained", "pvedisk status 400", "pvedisk clean 400"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("missing appliance diagnostic %q: %v", want, err)
		}
	}
	after, err := os.ReadFile(m.statePath(400))
	if err != nil || string(before) != string(after) {
		t.Fatal("active-appliance diagnostic changed session state")
	}
}

func TestUnmountWorkerFailureShowsNextAction(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux process identities")
	}
	for _, tc := range []struct {
		name, response string
		want           []string
	}{
		{"blocked", "your disk /dev/example is open in process 123; protection is retained\n", []string{"process 123", "pvedisk unmount 400"}},
		{"response lost", "", []string{"before cleanup was confirmed", "pvedisk status 400", "pvedisk clean 400"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, s := diagnosticSession(t)
			var err error
			s.Worker, err = identity(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			if err := m.save(s); err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("unix", m.socketPath(400))
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			done := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					done <- err
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(time.Second))
				token, err := bufio.NewReader(conn).ReadString('\n')
				if err == nil && strings.TrimSpace(token) != s.Token {
					err = fmt.Errorf("wrong ownership token")
				}
				if err == nil && tc.response != "" {
					_, err = fmt.Fprint(conn, tc.response)
				}
				done <- err
			}()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err = m.Unmount(ctx, 400)
			if err == nil {
				t.Fatal("worker failure accepted")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("missing cleanup action %q: %v", want, err)
				}
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if _, err := m.Status(400); err != nil {
				t.Fatal("worker failure discarded session")
			}
		})
	}
}
