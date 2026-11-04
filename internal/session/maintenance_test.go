package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/jeeftor/pvedisk/internal/proxmox"
)

type maintenanceFixture struct {
	disks   []proxmox.Disk
	calls   []string
	failAt  string
	failure error
	owned   bool
}

func (f *maintenanceFixture) step(name string) error {
	f.calls = append(f.calls, name)
	if f.failAt == name {
		return f.failure
	}
	return nil
}

func (f *maintenanceFixture) Inspect(context.Context, int) ([]proxmox.Disk, error) {
	return f.disks, f.step("inspect")
}

func (f *maintenanceFixture) Shutdown(context.Context, int) error { return f.step("shutdown") }

func (f *maintenanceFixture) Acquire(context.Context, int, string) ([]proxmox.Disk, error) {
	err := f.step("acquire")
	if err == nil {
		f.owned = true
	}
	return f.disks, err
}

func (f *maintenanceFixture) Release(context.Context, int, string) error {
	err := f.step("release")
	if err == nil {
		f.owned = false
	}
	return err
}

func maintenanceState(t *testing.T) (*Manager, *State, *maintenanceFixture) {
	t.Helper()
	m := testManager(t)
	disk := filepath.Join(t.TempDir(), "guest.img")
	if err := os.WriteFile(disk, []byte("unchanged guest disk"), 0600); err != nil {
		t.Fatal(err)
	}
	s := &State{VMID: 400, Token: strings.Repeat("a", 64), Phase: "selinux-relabel-pending"}
	if err := m.save(s); err != nil {
		t.Fatal(err)
	}
	f := &maintenanceFixture{disks: []proxmox.Disk{{Path: disk, Format: "raw"}}, failure: errors.New("fixture failure")}
	return m, s, f
}

func TestOfflineMaintenanceLifecycleAndCleanup(t *testing.T) {
	m, s, fixture := maintenanceState(t)
	unusedCalls := 0
	err := m.locked(func() error {
		return m.maintain(context.Background(), s, "selinux-relabel", fixture, func([]proxmox.Disk) error {
			unusedCalls++
			if !fixture.owned {
				t.Fatal("disk access guard ran without startup protection")
			}
			return fixture.step("unused")
		}, func(state *State) error {
			if !fixture.owned || state.Phase != "selinux-relabel" || !state.ProtectionAttempted || len(state.DiskIdentities) != 1 {
				t.Fatal("maintenance ran without recorded protected disk ownership")
			}
			return fixture.step("operation")
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"inspect", "shutdown", "acquire", "unused", "operation", "unused", "release"}
	if !reflect.DeepEqual(fixture.calls, want) || unusedCalls != 2 || fixture.owned {
		t.Fatalf("unexpected maintenance lifecycle: %v", fixture.calls)
	}
	if _, err := os.Stat(m.statePath(s.VMID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("successful maintenance left a repair session")
	}
	data, err := os.ReadFile(fixture.disks[0].Path)
	if err != nil || string(data) != "unchanged guest disk" {
		t.Fatal("lifecycle tests changed guest disk bytes")
	}
}

func TestOfflineMaintenanceFailuresRetainRecoveryState(t *testing.T) {
	for _, failure := range []string{"inspect", "shutdown", "acquire", "unused-before", "operation", "unused-after", "release"} {
		t.Run(failure, func(t *testing.T) {
			m, s, fixture := maintenanceState(t)
			fixture.failAt = failure
			unusedCalls := 0
			operationCalls := 0
			err := m.locked(func() error {
				return m.maintain(context.Background(), s, "selinux-relabel", fixture, func([]proxmox.Disk) error {
					unusedCalls++
					if (failure == "unused-before" && unusedCalls == 1) || (failure == "unused-after" && unusedCalls == 2) {
						return fixture.failure
					}
					return nil
				}, func(*State) error {
					operationCalls++
					return fixture.step("operation")
				})
			})
			if !errors.Is(err, fixture.failure) {
				t.Fatalf("lost maintenance failure: %v", err)
			}
			stored, err := m.Status(s.VMID)
			if err != nil || stored.Phase != "failed" {
				t.Fatalf("failed operation lost recovery state: %+v, %v", stored, err)
			}
			if failure != "inspect" && failure != "shutdown" && !stored.ProtectionAttempted {
				t.Fatal("failed operation lost protection intent")
			}
			if fixture.owned && fixture.calls[len(fixture.calls)-1] == "release" && failure != "release" {
				t.Fatal("maintenance failure attempted to release protection")
			}
			if failure == "inspect" || failure == "shutdown" || failure == "acquire" || failure == "unused-before" {
				if operationCalls != 0 {
					t.Fatal("unsafe lifecycle transition reached guest mutation")
				}
			}
			if failure == "operation" || failure == "unused-before" || failure == "unused-after" || failure == "release" {
				if !fixture.owned {
					t.Fatal("failure released startup protection")
				}
			}
		})
	}
}

func TestOfflineMaintenanceCanceledBeforeMutationRetainsProtection(t *testing.T) {
	m, s, fixture := maintenanceState(t)
	ctx, cancel := context.WithCancel(context.Background())
	err := m.locked(func() error {
		return m.maintain(ctx, s, "selinux-relabel", fixture, func([]proxmox.Disk) error {
			cancel()
			return nil
		}, func(*State) error {
			t.Fatal("canceled maintenance reached mutation")
			return nil
		})
	})
	if !errors.Is(err, context.Canceled) || !fixture.owned {
		t.Fatalf("canceled operation did not preserve protection: %v", err)
	}
	stored, err := m.Status(s.VMID)
	if err != nil || stored.Phase != "failed" || !stored.ProtectionAttempted {
		t.Fatal("canceled protected operation lost recovery state")
	}
}

func TestCanceledOfflineUtilitiesDoNotCreateState(t *testing.T) {
	for _, operation := range []func(*Manager, context.Context) error{
		func(m *Manager, ctx context.Context) error { return m.ScheduleSELinuxRelabel(ctx, 400, "/missing") },
		func(m *Manager, ctx context.Context) error {
			return m.ResetRoot(ctx, 400, "/missing", []byte("secret"))
		},
	} {
		m := &Manager{root: filepath.Join(t.TempDir(), "not-created")}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := operation(m, ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled operation returned %v", err)
		}
		if _, err := os.Stat(m.root); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("canceled operation created state")
		}
	}
}

func TestPasswordCustomizationUsesPrivateFileAndScrubsIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX command fixture")
	}
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failed], func(t *testing.T) {
			m, state, fixture := maintenanceState(t)
			state.Disks = fixture.disks
			dir := t.TempDir()
			arguments := filepath.Join(dir, "arguments")
			password := "private-test-password"
			program := "#!/bin/sh\nexec \"$PVEDISK_TEST_CUSTOMIZE_BINARY\" -test.run '^TestMaintenancePasswordChild$' -- \"$@\"\n"
			if err := os.WriteFile(filepath.Join(dir, "virt-customize"), []byte(program), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir)
			t.Setenv("PVEDISK_TEST_CUSTOMIZE_ARGS", arguments)
			t.Setenv("PVEDISK_TEST_CUSTOMIZE_CHILD", "1")
			t.Setenv("PVEDISK_TEST_CUSTOMIZE_BINARY", os.Args[0])
			t.Setenv("PVEDISK_TEST_CUSTOMIZE_EXIT", map[bool]string{false: "0", true: "7"}[failed])
			if err := m.resetRootOffline(context.Background(), state, []byte(password)); (err != nil) != failed {
				t.Fatalf("customization failure=%t, got %v", failed, err)
			}
			data, err := os.ReadFile(arguments)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"--format", "raw", "-a", fixture.disks[0].Path, "--root-password", "file:" + state.PasswordFile}
			if !reflect.DeepEqual(strings.Split(strings.TrimSuffix(string(data), "\n"), "\n"), want) || strings.Contains(string(data), password) {
				t.Fatalf("password was exposed or incorrect command arguments: %q", data)
			}
			if _, err := os.Stat(state.PasswordFile); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("password temporary file survived customization")
			}
			stored, err := m.Status(state.VMID)
			if err != nil || stored.PasswordFile != state.PasswordFile {
				t.Fatal("password file recovery intent was not recorded")
			}
		})
	}
}

func TestMaintenancePasswordChild(t *testing.T) {
	if os.Getenv("PVEDISK_TEST_CUSTOMIZE_CHILD") != "1" {
		return
	}
	var arguments []string
	for index, argument := range os.Args {
		if argument == "--" {
			arguments = os.Args[index+1:]
			break
		}
	}
	if len(arguments) != 6 || arguments[4] != "--root-password" || !strings.HasPrefix(arguments[5], "file:") {
		t.Fatal("password customization did not use a password file")
	}
	path := strings.TrimPrefix(arguments[5], "file:")
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Fatal("password file was not private during execution")
	}
	data, err := os.ReadFile(path)
	defer clear(data)
	if err != nil || string(data) != "private-test-password" {
		t.Fatal("customization did not receive the password through its file")
	}
	if os.Getenv("LIBGUESTFS_BACKEND") != "direct" {
		t.Fatal("customization did not use the direct backend")
	}
	if err := os.WriteFile(os.Getenv("PVEDISK_TEST_CUSTOMIZE_ARGS"), []byte(strings.Join(arguments, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("PVEDISK_TEST_CUSTOMIZE_EXIT") == "7" {
		os.Exit(7)
	}
}
