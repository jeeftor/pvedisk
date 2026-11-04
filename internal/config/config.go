// Package config manages the explicitly selected pvedisk configuration file.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/viper"
)

// DefaultPath is the system configuration location. No working-directory search is used.
const DefaultPath = "/etc/pvedisk/config.yaml"

// Config contains the operator-selected host destination for repair mounts.
type Config struct {
	MountRoot string `mapstructure:"mount_root" yaml:"mount_root"`
}

// ValidateRoot resolves a destination without creating it and checks its nearest
// existing ancestor for directory access. A nonexistent destination needs explicit
// operator approval before the caller creates it.
func ValidateRoot(path string) (canonical string, exists bool, err error) {
	if !filepath.IsAbs(path) {
		return "", false, errors.New("your mount root must be an absolute directory")
	}
	clean := filepath.Clean(path)
	if clean == string(filepath.Separator) {
		return "", false, errors.New("your mount root cannot be /")
	}
	ancestor := clean
	var suffix []string
	for {
		info, statErr := os.Lstat(ancestor)
		if statErr == nil {
			resolved, resolveErr := filepath.EvalSymlinks(ancestor)
			if resolveErr != nil {
				return "", false, fmt.Errorf("resolve your mount root: %w", resolveErr)
			}
			if info.Mode()&os.ModeSymlink != 0 {
				info, statErr = os.Stat(resolved)
				if statErr != nil {
					return "", false, fmt.Errorf("inspect your mount root: %w", statErr)
				}
			}
			if !info.IsDir() {
				return "", false, fmt.Errorf("your mount root ancestor %q is not a directory", ancestor)
			}
			if accessErr := syscall.Access(resolved, 7); accessErr != nil {
				return "", false, fmt.Errorf("access your mount root %q: %w", resolved, accessErr)
			}
			canonical = resolved
			for i := len(suffix) - 1; i >= 0; i-- {
				canonical = filepath.Join(canonical, suffix[i])
			}
			if canonical == string(filepath.Separator) {
				return "", false, errors.New("your mount root resolves to /")
			}
			return canonical, len(suffix) == 0, nil
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return "", false, fmt.Errorf("inspect your mount root: %w", statErr)
		}
		suffix = append(suffix, filepath.Base(ancestor))
		ancestor = filepath.Dir(ancestor)
	}
}

// Load reads only path and requires an existing, accessible destination.
func Load(path string) (Config, error) {
	var c Config
	if err := checkFile(path); err != nil {
		return c, err
	}
	v := viper.New()
	v.SetConfigFile(path)
	v.SetConfigType("yaml")
	if err := v.ReadInConfig(); err != nil {
		return c, fmt.Errorf("read your configuration %q: %w", path, err)
	}
	if err := v.UnmarshalExact(&c); err != nil {
		return c, fmt.Errorf("decode your configuration: %w", err)
	}
	root, exists, err := ValidateRoot(c.MountRoot)
	if err != nil {
		return c, err
	}
	if !exists {
		return c, fmt.Errorf("your configured mount root %q does not exist", root)
	}
	c.MountRoot = root
	return c, nil
}

// Save atomically replaces path with a private configuration file. Your mount
// root must already exist; this function never creates a mount destination.
func Save(path string, c Config) error {
	if runtime.GOOS == "linux" && os.Geteuid() != 0 {
		return errors.New("run setup as root to save your root-owned configuration")
	}
	root, exists, err := ValidateRoot(c.MountRoot)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("your mount root %q must be created before saving", root)
	}
	if !filepath.IsAbs(path) {
		return errors.New("your configuration path must be absolute")
	}
	if err := checkFile(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return fmt.Errorf("create your configuration directory: %w", err)
	}
	f, err := os.CreateTemp(dir, ".pvedisk-config-*")
	if err != nil {
		return fmt.Errorf("create your temporary configuration: %w", err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	defer f.Close()
	// Quote the path so punctuation cannot become YAML configuration syntax.
	if _, err := fmt.Fprintf(f, "mount_root: %s\n", strconv.Quote(root)); err != nil {
		return fmt.Errorf("write your configuration: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("synchronize your configuration: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close your configuration: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace your configuration: %w", err)
	}
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open your configuration directory: %w", err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("synchronize your configuration directory: %w", err)
	}
	return nil
}

func checkFile(path string) error {
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return errors.New("your configuration path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect your configuration %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("your configuration must be a regular file, not a symbolic link")
	}
	if info.Mode().Perm()&0022 != 0 {
		return errors.New("your configuration must not be writable by your group or other users")
	}
	if runtime.GOOS == "linux" {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 {
			return errors.New("your configuration must be owned by root")
		}
	}
	return nil
}
