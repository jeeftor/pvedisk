package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestValidateRoot(t *testing.T) {
	base := t.TempDir()
	canonical, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", ".", "relative/path", "/", "/tmp/.."} {
		t.Run("invalid_"+path, func(t *testing.T) {
			if _, _, err := ValidateRoot(path); err == nil {
				t.Fatalf("accepted %q", path)
			}
		})
	}
	got, exists, err := ValidateRoot(base)
	if err != nil || !exists || got != canonical {
		t.Fatalf("existing directory: got %q, %v, %v", got, exists, err)
	}
	missing := filepath.Join(base, "missing", "nested")
	got, exists, err = ValidateRoot(missing)
	if err != nil || exists || got != filepath.Join(canonical, "missing", "nested") {
		t.Fatalf("missing directory: got %q, %v, %v", got, exists, err)
	}
	if _, err := os.Stat(filepath.Join(base, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("validation mutated the filesystem: %v", err)
	}
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, []byte("content"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{file, filepath.Join(file, "child")} {
		if _, _, err := ValidateRoot(path); err == nil {
			t.Fatalf("accepted nondirectory %q", path)
		}
	}
}

func TestValidateRootSymlinks(t *testing.T) {
	base := t.TempDir()
	link := filepath.Join(base, "link")
	if err := os.Symlink(base, link); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	got, exists, err := ValidateRoot(filepath.Join(link, "missing"))
	if err != nil || exists || got != filepath.Join(canonical, "missing") {
		t.Fatalf("resolve missing destination through symlink: %q, %v, %v", got, exists, err)
	}
	rootLink := filepath.Join(base, "root-link")
	if err := os.Symlink("/", rootLink); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ValidateRoot(rootLink); err == nil {
		t.Fatal("accepted a mount root resolving to /")
	}
	broken := filepath.Join(base, "broken")
	if err := os.Symlink(filepath.Join(base, "not-there"), broken); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ValidateRoot(broken); err == nil {
		t.Fatal("accepted a broken symlink")
	}
}

func TestValidateRootAccess(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses ordinary directory access permissions")
	}
	root := filepath.Join(t.TempDir(), "inaccessible")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0700) })
	if _, _, err := ValidateRoot(root); err == nil {
		t.Fatal("accepted an inaccessible directory")
	}
	if _, _, err := ValidateRoot(filepath.Join(root, "missing")); err == nil {
		t.Fatal("accepted a missing directory below an inaccessible ancestor")
	}
}

func requireConfigOwner(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "linux" && os.Geteuid() != 0 {
		t.Skip("root-owned configuration writes require root on Linux")
	}
}

func TestSaveLoadAndReplace(t *testing.T) {
	requireConfigOwner(t)
	base := t.TempDir()
	root := filepath.Join(base, "mount: # root")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(base, "config", "config.yaml")
	if err := Save(path, Config{MountRoot: root}); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if c.MountRoot != want {
		t.Fatalf("mount root = %q, want %q", c.MountRoot, want)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("configuration permissions: %v, %v", info, err)
	}
	old, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(base, "never-create")
	if err := Save(path, Config{MountRoot: missing}); err == nil {
		t.Fatal("saved nonexistent destination")
	}
	current, err := os.ReadFile(path)
	if err != nil || string(current) != string(old) {
		t.Fatalf("failed save changed existing config: %v", err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed save created mount root: %v", err)
	}
	if err := Save(path, Config{MountRoot: base}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary files remain: %v, %v", entries, err)
	}
}

func TestLoadExplicitLocationAndRejectUnsafeConfig(t *testing.T) {
	requireConfigOwner(t)
	base := t.TempDir()
	path := filepath.Join(base, "config.yaml")
	if _, err := Load(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing config error does not retain os.ErrNotExist: %v", err)
	}
	if err := Save(path, Config{MountRoot: base}); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link.yaml")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(link); err == nil {
		t.Fatal("loaded symlink configuration")
	}
	if err := Save(link, Config{MountRoot: base}); err == nil {
		t.Fatal("replaced symlink configuration")
	}
	if err := os.Chmod(path, 0666); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("loaded world-writable configuration")
	}
	if _, err := Load("config.yaml"); err == nil {
		t.Fatal("accepted relative configuration path")
	}
}
