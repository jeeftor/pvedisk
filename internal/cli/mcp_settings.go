package cli

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"
	"golang.org/x/sys/unix"
)

const maxMCPSettingsSize = 64 * 1024

type mcpSettings struct {
	Listen    string `yaml:"listen"`
	TokenFile string `yaml:"token_file,omitempty"`
	NoToken   bool   `yaml:"no_token,omitempty"`
}

func mcpSettingsPath(explicit string) (string, error) {
	if explicit != "" {
		return filepath.Abs(explicit)
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Abs(filepath.Join(base, "pvedisk", "mcp.yaml"))
}

func loadMCPSettings(path string) (mcpSettings, error) {
	settings, data, _, err := loadMCPSettingsSnapshot(path)
	clear(data)
	return settings, err
}

// loadMCPSettingsSnapshot retains the exact parsed source and opened-file
// identity so setup can refuse to overwrite a profile edited during setup.
// The caller owns the returned source bytes and clears them after use.
func loadMCPSettingsSnapshot(path string) (mcpSettings, []byte, os.FileInfo, error) {
	settings := mcpSettings{Listen: "127.0.0.1:8766"}
	resolved, err := mcpSettingsPath(path)
	if err != nil {
		return settings, nil, nil, err
	}
	file, err := openMCPPrivateFile(resolved)
	if errors.Is(err, os.ErrNotExist) && path == "" {
		return settings, nil, nil, nil
	}
	if err != nil {
		return settings, nil, nil, fmt.Errorf("read your MCP settings: %w", &os.PathError{Op: "open", Path: resolved, Err: err})
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return settings, nil, nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, maxMCPSettingsSize+1))
	if err != nil {
		clear(data)
		return settings, nil, nil, errors.New("could not read your MCP settings")
	}
	if len(data) > maxMCPSettingsSize {
		clear(data)
		return settings, nil, nil, errors.New("your MCP settings exceed the 64-kilobyte limit")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&settings); err != nil {
		clear(data)
		return settings, nil, nil, errors.New("your MCP settings must be valid YAML with supported fields")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		clear(data)
		return settings, nil, nil, errors.New("your MCP settings must contain one YAML document")
	}
	if settings.TokenFile != "" && !filepath.IsAbs(settings.TokenFile) {
		settings.TokenFile = filepath.Join(filepath.Dir(resolved), settings.TokenFile)
	}
	return settings, data, info, nil
}

func overrideMCPSettings(cmd *cobra.Command, saved, flags mcpSettings) mcpSettings {
	if cmd.Flags().Changed("listen") {
		saved.Listen = flags.Listen
	}
	if cmd.Flags().Changed("token-file") {
		saved.TokenFile = flags.TokenFile
		if flags.TokenFile != "" {
			saved.NoToken = false
		}
	}
	if cmd.Flags().Changed("no-token") {
		saved.NoToken = flags.NoToken
		if flags.NoToken {
			saved.TokenFile = ""
		}
	}
	return saved
}

func (settings mcpSettings) validate() error {
	if settings.TokenFile != "" && settings.NoToken {
		return errors.New("your MCP settings cannot combine token_file and no_token")
	}
	host, port, err := net.SplitHostPort(settings.Listen)
	if err != nil || port == "" {
		return errors.New("your MCP listen address must contain a host and port")
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 0 || number > 65535 {
		return errors.New("your MCP listen port must be between 0 and 65535")
	}
	ip := net.ParseIP(host)
	if ip == nil && (strings.ContainsAny(host, "/\\?#@[]:") || strings.ContainsFunc(host, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })) {
		return errors.New("your MCP listen address contains an invalid hostname")
	}
	// Hostnames may resolve to loopback through local DNS or hosts files. The
	// HTTP server checks the actual bound address before serving requests.
	if (host == "" || (ip != nil && !ip.IsLoopback())) && settings.TokenFile == "" && !settings.NoToken {
		return errors.New("your non-loopback MCP listener requires a token file or explicit --no-token")
	}
	return nil
}

func openMCPPrivateFile(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() || int(stat.Uid) != os.Geteuid() || info.Mode().Perm()&0022 != 0 {
		file.Close()
		return nil, errors.New("your configuration must be a regular file owned by your effective user and not writable by your group or other users")
	}
	return file, nil
}

// writeMCPPrivateFile retains a private byte-exact backup before replacing an
// existing file. The final recheck detects concurrent edits on a best-effort basis.
func writeMCPPrivateFile(path string, data []byte) error {
	if !filepath.IsAbs(path) {
		return errors.New("your configuration path must be absolute")
	}
	old, original, err := readMCPPrivateSnapshot(path)
	if err != nil {
		return err
	}
	defer clear(old)
	return writeMCPPrivateFileFromSnapshot(path, data, old, original)
}

// writeMCPPrivateFileFromSnapshot checks the source captured before a client
// configuration was parsed. The caller retains ownership of old and clears it.
func writeMCPPrivateFileFromSnapshot(path string, data, old []byte, original os.FileInfo) error {
	if !filepath.IsAbs(path) {
		return errors.New("your configuration path must be absolute")
	}
	current, currentInfo, err := readMCPPrivateSnapshot(path)
	if err != nil {
		return err
	}
	unchanged := (original == nil && currentInfo == nil) || (original != nil && currentInfo != nil && os.SameFile(original, currentInfo) && bytes.Equal(old, current))
	clear(current)
	if !unchanged {
		return errors.New("your configuration changed during setup; no replacement was made")
	}
	if original != nil && bytes.Equal(old, data) {
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	temporary, err := writeMCPTemporary(dir, ".pvedisk-mcp-*", data)
	if err != nil {
		return err
	}
	defer os.Remove(temporary)
	if original != nil {
		if _, err := writeMCPTemporary(dir, filepath.Base(path)+".backup-*", old); err != nil {
			return err
		}
		if err := syncMCPDirectory(dir); err != nil {
			return err
		}
	}
	current, currentInfo, err = readMCPPrivateSnapshot(path)
	if err != nil {
		return err
	}
	unchanged = (original == nil && currentInfo == nil) || (original != nil && currentInfo != nil && os.SameFile(original, currentInfo) && bytes.Equal(old, current))
	clear(current)
	if !unchanged {
		return errors.New("your configuration changed during setup; no replacement was made")
	}
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	return syncMCPDirectory(dir)
}

func readMCPPrivateSnapshot(path string) ([]byte, os.FileInfo, error) {
	file, err := openMCPPrivateFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	data, err := io.ReadAll(file)
	if err != nil {
		clear(data)
		return nil, nil, err
	}
	return data, info, nil
}

func writeMCPTemporary(dir, pattern string, data []byte) (name string, err error) {
	file, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	name = file.Name()
	defer func() {
		file.Close()
		if err != nil {
			os.Remove(name)
		}
	}()
	if err = file.Chmod(0600); err != nil {
		return name, err
	}
	if _, err = file.Write(data); err != nil {
		return name, err
	}
	if err = file.Sync(); err != nil {
		return name, err
	}
	err = file.Close()
	return name, err
}

func syncMCPDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// ensureMCPToken creates a random token only when the selected path is missing.
// Existing contents are preserved; the caller validates them with ReadToken.
func ensureMCPToken(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("your token file path must be absolute")
	}
	file, err := openMCPPrivateFile(path)
	if err == nil {
		return file.Close()
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	random := make([]byte, 32)
	defer clear(random)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	encoded := make([]byte, hex.EncodedLen(len(random))+1)
	defer clear(encoded)
	hex.Encode(encoded, random)
	encoded[len(encoded)-1] = '\n'
	file, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		// A concurrent setup won creation; validate that file rather than rotating it.
		file, err = openMCPPrivateFile(path)
		if err != nil {
			return err
		}
		return file.Close()
	}
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Chmod(0600); err != nil {
		return err
	}
	if _, err := file.Write(encoded); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return syncMCPDirectory(filepath.Dir(path))
}
