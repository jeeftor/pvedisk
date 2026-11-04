package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jeeftor/pvedisk/internal/config"
	"github.com/jeeftor/pvedisk/internal/session"
	"github.com/spf13/cobra"
)

func testOperations() operations {
	return operations{
		load:        func(string) (config.Config, error) { return config.Config{}, os.ErrNotExist },
		save:        func(string, config.Config) error { return nil },
		setupLock:   func(fn func() error) error { return fn() },
		hasSessions: func() (bool, error) { return false, nil },
		terminal:    func(*cobra.Command) bool { return false },
	}
}

func execute(ops operations, args ...string) (string, error) {
	return executeInput(ops, "", args...)
}

func executeInput(ops operations, input string, args ...string) (string, error) {
	var output bytes.Buffer
	command := newCommand(ops)
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetIn(strings.NewReader(input))
	command.SetArgs(args)
	err := command.Execute()
	return output.String(), err
}

func TestMountPromptsForMissingVMID(t *testing.T) {
	for _, tc := range []struct {
		name, input         string
		terminal, wantMount bool
	}{
		{"interactive", "400\n", true, true},
		{"invalid", "invalid\n", true, false},
		{"empty", "\n", true, false},
		{"cancelled", "", true, false},
		{"noninteractive", "400\n", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ops := testOperations()
			ops.load = func(string) (config.Config, error) { return config.Config{MountRoot: "/repairs"}, nil }
			ops.terminal = func(*cobra.Command) bool { return tc.terminal }
			called := false
			ops.mount = func(_ context.Context, id int, _ string, ro bool) error {
				if id != 400 || !ro {
					t.Fatalf("id=%d ro=%v", id, ro)
				}
				called = true
				return nil
			}
			ops.status = func(int) (*session.State, error) { return &session.State{Public: "/repairs/vm400"}, nil }
			output, err := executeInput(ops, tc.input, "mount", "--ro")
			if called != tc.wantMount || (err == nil) != tc.wantMount {
				t.Fatalf("called=%v output=%q err=%v", called, output, err)
			}
			if tc.terminal && !strings.Contains(output, "VM ID:") {
				t.Fatalf("missing prompt: %q", output)
			}
			if !tc.terminal && strings.Contains(output, "VM ID:") {
				t.Fatal("prompted without a terminal")
			}
		})
	}
}

func TestMissingConfigurationDoesNotStartVMOperations(t *testing.T) {
	for _, args := range [][]string{nil, {"mount"}, {"mount", "400"}, {"reset-root", "400"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			ops := testOperations()
			ops.mount = func(context.Context, int, string, bool) error { t.Fatal("mount called"); return nil }
			ops.password = func(*cobra.Command) ([]byte, error) { t.Fatal("password prompted"); return nil, nil }
			ops.reset = func(context.Context, int, string, []byte) error { t.Fatal("reset called"); return nil }
			output, err := execute(ops, args...)
			if err == nil || !strings.Contains(output, "setup --mount-root") {
				t.Fatalf("output=%q err=%v", output, err)
			}
		})
	}
}

func TestConfiguredBareCommandShowsHelpAndDestination(t *testing.T) {
	ops := testOperations()
	ops.load = func(string) (config.Config, error) { return config.Config{MountRoot: "/mnt/repairs"}, nil }
	output, err := execute(ops)
	if err != nil || !strings.Contains(output, "Configured destination: /mnt/repairs") || !strings.Contains(output, "Available Commands:") {
		t.Fatalf("output=%q err=%v", output, err)
	}
}

func TestNoninteractiveSetupCreatesOnlyConfirmedDestination(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "repairs")
	ops := testOperations()
	var saved config.Config
	var savedPath string
	ops.save = func(path string, cfg config.Config) error { savedPath, saved = path, cfg; return nil }
	_, err := execute(ops, "--config", "/explicit/config.yaml", "setup", "--mount-root", destination, "--yes")
	if err != nil {
		t.Fatal(err)
	}
	canonical, exists, err := config.ValidateRoot(destination)
	if err != nil || !exists || saved.MountRoot != canonical || savedPath != "/explicit/config.yaml" {
		t.Fatalf("saved=%+v path=%s exists=%v err=%v", saved, savedPath, exists, err)
	}
}

func TestSetupWithoutConfirmationDoesNotCreate(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "repairs")
	ops := testOperations()
	ops.save = func(string, config.Config) error { t.Fatal("configuration saved"); return nil }
	_, err := execute(ops, "setup", "--mount-root", destination)
	if err == nil {
		t.Fatal("noninteractive setup accepted without --yes")
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("directory created: %v", err)
	}
}

func TestActiveAndUnresolvedSessionsPreventDestinationChange(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "configured", true: "damaged-config"}[corrupt], func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "repairs")
			ops := testOperations()
			ops.hasSessions = func() (bool, error) { return true, nil }
			ops.load = func(string) (config.Config, error) {
				if corrupt {
					return config.Config{}, errors.New("invalid yaml")
				}
				return config.Config{MountRoot: "/previous"}, nil
			}
			ops.save = func(string, config.Config) error { t.Fatal("saved during repair"); return nil }
			_, err := execute(ops, "setup", "--mount-root", destination, "--yes")
			if err == nil || !strings.Contains(err.Error(), "unresolved") {
				t.Fatalf("err=%v", err)
			}
			if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("directory created: %v", err)
			}
		})
	}
}

func TestUnmountAndRecoveryIgnoreDamagedConfiguration(t *testing.T) {
	for _, action := range []string{"unmount", "recover"} {
		t.Run(action, func(t *testing.T) {
			ops := testOperations()
			ops.load = func(string) (config.Config, error) { t.Fatal("configuration read"); return config.Config{}, nil }
			called := false
			run := func(_ context.Context, id int) error { called = id == 400; return nil }
			ops.unmount, ops.recover = run, run
			output, err := execute(ops, action, "400")
			if err != nil || !called || !strings.Contains(output, "You can now run: qm start 400") {
				t.Fatalf("called=%v output=%q err=%v", called, output, err)
			}
		})
	}
}

func TestWizardCancellationAndValidation(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "new-destination")
	m := newSetupModel(destination)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(setupModel)
	if !m.confirming || !m.missing || !strings.Contains(m.View(), "vm400-extra") || !strings.Contains(m.View(), "Create your missing directory") {
		t.Fatalf("unexpected view: %s", m.View())
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m = updated.(setupModel)
	if !m.cancelled || m.confirmed {
		t.Fatal("cancellation not respected")
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("wizard mutated destination: %v", err)
	}
	invalid := newSetupModel("/")
	updated, _ = invalid.Update(tea.KeyMsg{Type: tea.KeyEnter})
	invalid = updated.(setupModel)
	if invalid.err == nil || invalid.confirming {
		t.Fatal("invalid root accepted")
	}
}

func TestMountReadOnlyIsExplicit(t *testing.T) {
	for _, ro := range []bool{false, true} {
		ops := testOperations()
		ops.load = func(string) (config.Config, error) { return config.Config{MountRoot: "/repairs"}, nil }
		called := false
		ops.mount = func(_ context.Context, id int, root string, readOnly bool) error {
			called = id == 400 && root == config.DefaultPath && readOnly == ro
			return nil
		}
		ops.status = func(int) (*session.State, error) { return &session.State{Public: "/repairs/vm400"}, nil }
		args := []string{"mount", "400"}
		if ro {
			args = append(args, "--ro")
		}
		if _, err := execute(ops, args...); err != nil || !called {
			t.Fatalf("ro=%v called=%v err=%v", ro, called, err)
		}
	}
}

func TestDamagedConfigurationPreventsDiskActions(t *testing.T) {
	ops := testOperations()
	ops.load = func(string) (config.Config, error) { return config.Config{}, errors.New("invalid configuration") }
	ops.mount = func(context.Context, int, string, bool) error { t.Fatal("mount called"); return nil }
	_, err := execute(ops, "mount", "400")
	if err == nil || !strings.Contains(err.Error(), "invalid configuration") {
		t.Fatalf("err=%v", err)
	}
}

func TestPasswordIsPassedPrivatelyAndCleared(t *testing.T) {
	ops := testOperations()
	ops.load = func(string) (config.Config, error) { return config.Config{MountRoot: "/repairs"}, nil }
	password := []byte("private-test-password")
	ops.password = func(*cobra.Command) ([]byte, error) { return password, nil }
	called := false
	ops.reset = func(_ context.Context, id int, path string, value []byte) error {
		called = id == 400 && path == config.DefaultPath && string(value) == "private-test-password"
		return nil
	}
	output, err := execute(ops, "reset-root", "400")
	if err != nil || !called || strings.Contains(output, "private-test-password") {
		t.Fatalf("called=%v output=%q err=%v", called, output, err)
	}
	for _, b := range password {
		if b != 0 {
			t.Fatal("password memory retained")
		}
	}
}

func TestInvalidVMIDDoesNotCallBackend(t *testing.T) {
	ops := testOperations()
	ops.unmount = func(context.Context, int) error { t.Fatal("backend called"); return nil }
	for _, id := range []string{"99", "1000000000", "-1", "400;true"} {
		if _, err := execute(ops, "unmount", id); err == nil {
			t.Fatalf("accepted %q", id)
		}
	}
}
