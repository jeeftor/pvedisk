package cli

import (
	"bytes"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"
	"golang.org/x/sys/unix"
)

func TestMCPSettingsPathsAndMissingFiles(t *testing.T) {
	base := t.TempDir()
	t.Setenv("HOME", base)
	t.Setenv("XDG_CONFIG_HOME", "")
	path, err := mcpSettingsPath("")
	if err != nil || path != filepath.Join(base, ".config", "pvedisk", "mcp.yaml") {
		t.Fatalf("home settings path = %q, %v", path, err)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "xdg"))
	path, err = mcpSettingsPath("")
	if err != nil || path != filepath.Join(base, "xdg", "pvedisk", "mcp.yaml") {
		t.Fatalf("XDG settings path = %q, %v", path, err)
	}
	settings, err := loadMCPSettings("")
	if err != nil || settings != (mcpSettings{Listen: "127.0.0.1:8766"}) {
		t.Fatalf("missing default settings = %+v, %v", settings, err)
	}
	if _, err := loadMCPSettings(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("explicit missing configuration did not fail: %v", err)
	}
	explicit, err := mcpSettingsPath("chosen.yaml")
	want, absErr := filepath.Abs("chosen.yaml")
	if err != nil || absErr != nil || explicit != want {
		t.Fatalf("explicit relative path = %q, %v", explicit, err)
	}
}

func TestMCPLoadSettingsAndRelativeToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.yaml")
	if err := os.WriteFile(path, []byte("listen: '[::1]:9000'\ntoken_file: secrets/token\nno_token: false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	settings, err := loadMCPSettings(path)
	if err != nil || settings.Listen != "[::1]:9000" || settings.TokenFile != filepath.Join(filepath.Dir(path), "secrets", "token") || settings.NoToken {
		t.Fatalf("loaded settings = %+v, %v", settings, err)
	}
}

func TestMCPLoadSettingsSnapshotPreservesBaseline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.yaml")
	source := []byte("# retain exact original bytes in backup\nlisten: 127.0.0.1:9000\ntoken_file: secrets/token\n")
	if err := os.WriteFile(path, source, 0600); err != nil {
		t.Fatal(err)
	}
	settings, raw, original, err := loadMCPSettingsSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(raw)
	info, err := os.Stat(path)
	if err != nil || original == nil || !os.SameFile(info, original) || !bytes.Equal(raw, source) {
		t.Fatal("settings snapshot did not preserve parsed file bytes and identity")
	}
	if settings.Listen != "127.0.0.1:9000" || settings.TokenFile != filepath.Join(filepath.Dir(path), "secrets", "token") {
		t.Fatalf("snapshot parsed incorrect settings: %+v", settings)
	}
	settings.Listen = "127.0.0.1:9001"
	updated, err := yaml.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeMCPPrivateFileFromSnapshot(path, updated, raw, original); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadMCPSettings(path)
	if err != nil || loaded != settings {
		t.Fatalf("snapshot save roundtrip: %+v, %v", loaded, err)
	}
	backups, err := filepath.Glob(path + ".backup-*")
	if err != nil || len(backups) != 1 {
		t.Fatal("settings snapshot save did not back up original")
	}
	backup, err := os.ReadFile(backups[0])
	if err != nil || !bytes.Equal(backup, source) {
		t.Fatal("settings snapshot backup lost original comments or bytes")
	}
}

func TestMCPLoadSettingsSnapshotRejectsConcurrentProfileEdit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.yaml")
	if err := os.WriteFile(path, []byte("listen: 127.0.0.1:9000\n"), 0600); err != nil {
		t.Fatal(err)
	}
	settings, raw, original, err := loadMCPSettingsSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(raw)
	concurrent := []byte("listen: 127.0.0.1:9999\n")
	if err := os.WriteFile(path, concurrent, 0600); err != nil {
		t.Fatal(err)
	}
	settings.NoToken = true
	updated, err := yaml.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeMCPPrivateFileFromSnapshot(path, updated, raw, original); err == nil {
		t.Fatal("setup overwrote a profile edited after loading")
	}
	actual, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(actual, concurrent) {
		t.Fatal("profile editor's changes were lost")
	}
}

func TestMCPLoadSettingsSnapshotMissingAndInvalid(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	settings, raw, original, err := loadMCPSettingsSnapshot("")
	if err != nil || settings.Listen != "127.0.0.1:8766" || raw != nil || original != nil {
		t.Fatal("missing default profile did not return an absent baseline")
	}
	path := filepath.Join(t.TempDir(), "mcp.yaml")
	_, raw, original, err = loadMCPSettingsSnapshot(path)
	var pathError *os.PathError
	if !errors.Is(err, os.ErrNotExist) || !errors.As(err, &pathError) || raw != nil || original != nil {
		t.Fatal("explicit missing profile did not return a missing-path error and absent baseline")
	}
	if err := os.WriteFile(path, []byte("unknown_field: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, raw, original, err = loadMCPSettingsSnapshot(path)
	if err == nil || raw != nil || original != nil {
		t.Fatal("invalid profile exposed a writable snapshot baseline")
	}
}

func TestMCPSettingsRejectsMalformedAndUnsafeFiles(t *testing.T) {
	for _, source := range []string{
		"", "listen: [", "listen: 127.0.0.1:8766\nunknown: true\n",
		"listen: 127.0.0.1:8766\n---\nlisten: 0.0.0.0:8766\n",
		"listen: 127.0.0.1:8766\n---\n", strings.Repeat("#", maxMCPSettingsSize+1),
	} {
		path := filepath.Join(t.TempDir(), "mcp.yaml")
		if err := os.WriteFile(path, []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadMCPSettings(path); err == nil {
			t.Fatalf("accepted invalid settings of length %d", len(source))
		}
	}
	base := t.TempDir()
	path := filepath.Join(base, "mcp.yaml")
	if err := os.WriteFile(path, []byte("listen: 127.0.0.1:8766\n"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(base, "fifo")
	if err := unix.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	for _, unsafe := range []string{base, link, fifo} {
		if _, err := loadMCPSettings(unsafe); err == nil {
			t.Fatalf("accepted unsafe settings path %s", unsafe)
		}
	}
	if err := os.Chmod(path, 0660); err != nil {
		t.Fatal(err)
	}
	if _, err := loadMCPSettings(path); err == nil {
		t.Fatal("accepted group-writable settings")
	}
}

func TestMCPSettingsFlagPrecedence(t *testing.T) {
	for _, test := range []struct {
		name  string
		saved mcpSettings
		args  []string
		want  mcpSettings
	}{
		{"unchanged", mcpSettings{Listen: "localhost:99", NoToken: true}, nil, mcpSettings{Listen: "localhost:99", NoToken: true}},
		{"boolean false", mcpSettings{Listen: "localhost:99", NoToken: true}, []string{"--no-token=false"}, mcpSettings{Listen: "localhost:99"}},
		{"token wins", mcpSettings{Listen: "localhost:99", NoToken: true}, []string{"--token-file=/new/token"}, mcpSettings{Listen: "localhost:99", TokenFile: "/new/token"}},
		{"no token wins", mcpSettings{Listen: "localhost:99", TokenFile: "/old/token"}, []string{"--no-token"}, mcpSettings{Listen: "localhost:99", NoToken: true}},
		{"listen wins", mcpSettings{Listen: "localhost:99"}, []string{"--listen=127.0.0.1:8766"}, mcpSettings{Listen: "127.0.0.1:8766"}},
		{"clear token", mcpSettings{Listen: "localhost:99", TokenFile: "/old/token"}, []string{"--token-file="}, mcpSettings{Listen: "localhost:99"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd := &cobra.Command{}
			var flags mcpSettings
			cmd.Flags().StringVar(&flags.Listen, "listen", "127.0.0.1:8766", "")
			cmd.Flags().StringVar(&flags.TokenFile, "token-file", "", "")
			cmd.Flags().BoolVar(&flags.NoToken, "no-token", false, "")
			if err := cmd.ParseFlags(test.args); err != nil {
				t.Fatal(err)
			}
			if got := overrideMCPSettings(cmd, test.saved, flags); got != test.want {
				t.Fatalf("resolved settings = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestMCPSettingsValidate(t *testing.T) {
	for _, settings := range []mcpSettings{
		{Listen: "127.0.0.1:0"}, {Listen: "[::1]:8766"}, {Listen: "localhost:8766"},
		{Listen: "local-loopback-alias:8766"},
		{Listen: "0.0.0.0:8766", NoToken: true}, {Listen: "example.test:8766", TokenFile: "/token"},
	} {
		if err := settings.validate(); err != nil {
			t.Fatalf("rejected valid settings %+v: %v", settings, err)
		}
	}
	for _, settings := range []mcpSettings{
		{}, {Listen: "127.0.0.1"}, {Listen: "127.0.0.1:"}, {Listen: "localhost:-1"},
		{Listen: "localhost:65536"}, {Listen: "localhost:http"}, {Listen: "0.0.0.0:8766"},
		{Listen: ":8766"}, {Listen: "[::]:8766"}, {Listen: "localhost:8766", TokenFile: "/token", NoToken: true},
		{Listen: "bad host:8766", TokenFile: "/token"}, {Listen: "bad/host:8766", TokenFile: "/token"},
	} {
		if err := settings.validate(); err == nil {
			t.Fatalf("accepted invalid settings %+v", settings)
		}
	}
}

func TestMCPPrivateWriterBackupAndIdenticalNoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	original := []byte("{\n// original comment\n\"number\":9007199254740993,\n}\n")
	replacement := []byte("{\"replaced\":true}\n")
	if err := writeMCPPrivateFile(path, original); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil || before.Mode().Perm() != 0600 {
		t.Fatalf("new configuration permissions: %v", err)
	}
	if err := writeMCPPrivateFile(path, original); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) || !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("identical write replaced the file")
	}
	backups, err := filepath.Glob(path + ".backup-*")
	if err != nil || len(backups) != 0 {
		t.Fatal("identical write created a backup")
	}
	if err := writeMCPPrivateFile(path, replacement); err != nil {
		t.Fatal(err)
	}
	backups, err = filepath.Glob(path + ".backup-*")
	if err != nil || len(backups) != 1 {
		t.Fatal("missing original backup")
	}
	backup, err := os.ReadFile(backups[0])
	if err != nil || !bytes.Equal(backup, original) {
		t.Fatal("backup did not preserve original bytes")
	}
	for _, private := range []string{path, backups[0]} {
		info, err := os.Stat(private)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("unsafe private file %s", private)
		}
	}
	current, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(current, replacement) {
		t.Fatal("replacement differs")
	}
}

func TestMCPPrivateWriterRejectsUnsafeTarget(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "config")
	if err := os.WriteFile(path, []byte("preserve me"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	for _, unsafe := range []string{base, link, "relative"} {
		if err := writeMCPPrivateFile(unsafe, []byte("new")); err == nil {
			t.Fatalf("accepted unsafe target %q", unsafe)
		}
	}
	current, err := os.ReadFile(path)
	if err != nil || string(current) != "preserve me" {
		t.Fatal("unsafe target write modified original")
	}
}

func TestMCPPrivateWriterRejectsChangedSnapshot(t *testing.T) {
	for _, test := range []struct {
		name           string
		missing        bool
		replace        bool
		unchangedMerge bool
	}{
		{name: "edited during merge"},
		{name: "edited during unchanged merge", unchangedMerge: true},
		{name: "replaced during merge", replace: true},
		{name: "created during merge", missing: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config")
			if !test.missing {
				if err := os.WriteFile(path, []byte("original bytes"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			old, original, err := readMCPPrivateSnapshot(path)
			if err != nil {
				t.Fatal(err)
			}
			defer clear(old)
			concurrent := []byte("concurrent editor bytes")
			if test.replace {
				// A same-content replacement still changes the file's identity.
				concurrent = []byte("original bytes")
				replacement := path + ".replacement"
				if err := os.WriteFile(replacement, concurrent, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(replacement, path); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, concurrent, 0600); err != nil {
				t.Fatal(err)
			}
			merged := []byte("merged configuration")
			if test.unchangedMerge {
				merged = old
			}
			if err := writeMCPPrivateFileFromSnapshot(path, merged, old, original); err == nil {
				t.Fatal("accepted a stale configuration snapshot")
			}
			actual, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(actual, concurrent) {
				t.Fatal("stale merge changed concurrent editor content")
			}
			backups, err := filepath.Glob(path + ".backup-*")
			if err != nil || len(backups) != 0 {
				t.Fatal("stale merge wrote a misleading backup")
			}
		})
	}
}

func TestEnsureMCPTokenCreatesAndReuses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "mcp.token")
	if err := ensureMCPToken(path); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil || len(first) != 65 || first[64] != '\n' {
		t.Fatalf("invalid generated token: %v", err)
	}
	if decoded, err := hex.DecodeString(string(first[:64])); err != nil || len(decoded) != 32 {
		t.Fatal("token does not encode 32 random bytes")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("generated token is not private")
	}
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := ensureMCPToken(path); err != nil {
				t.Errorf("reuse token: %v", err)
			}
		}()
	}
	wait.Wait()
	second, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatal("setup rotated an existing token")
	}
	link := filepath.Join(filepath.Dir(path), "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if err := ensureMCPToken(link); err == nil {
		t.Fatal("accepted token symlink")
	}
	if err := ensureMCPToken("relative"); err == nil {
		t.Fatal("accepted relative token path")
	}
}
