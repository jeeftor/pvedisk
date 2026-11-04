// Package cli provides the pvedisk command line and first-run setup wizard.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/jeeftor/pvedisk/internal/config"
	"github.com/jeeftor/pvedisk/internal/proxmox"
	"github.com/jeeftor/pvedisk/internal/session"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

type operations struct {
	hasSessions func() (bool, error)
	setupLock   func(func() error) error
	mount       func(context.Context, int, string, bool) error
	unmount     func(context.Context, int) error
	recover     func(context.Context, int) error
	reset       func(context.Context, int, string, []byte) error
	worker      func(context.Context, int) error
	status      func(int) (*session.State, error)
	doctor      func(context.Context) error
	load        func(string) (config.Config, error)
	save        func(string, config.Config) error
	terminal    func(*cobra.Command) bool
	password    func(*cobra.Command) ([]byte, error)
}

// Version identifies the build and is set by release builds using ldflags.
var Version = "0.1.0-dev"

// NewCommand constructs the CLI. Configuration is read only from the explicit
// path, or /etc/pvedisk/config.yaml, and never from the working directory.
func NewCommand() *cobra.Command {
	m := session.New()
	return newCommand(operations{
		hasSessions: m.HasSessions,
		setupLock:   m.WithSetupLock,
		mount:       m.Mount, unmount: m.Unmount, recover: m.Recover,
		reset: m.ResetRoot, worker: m.Worker,
		status: m.Status,
		doctor: proxmox.New().Doctor,
		load:   config.Load, save: config.Save,
		terminal: isTerminal, password: readPassword,
	})
}

func newCommand(ops operations) *cobra.Command {
	var configPath string
	root := &cobra.Command{
		Use: "pvedisk", Short: "Mount and repair stopped Proxmox Linux guests",
		Version:      Version,
		SilenceUsage: true, SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := ops.load(configPath)
			if errors.Is(err, os.ErrNotExist) {
				if !ops.terminal(cmd) {
					return missingConfig(cmd, configPath)
				}
				return runSetup(cmd, ops, configPath, "", false)
			}
			if err != nil {
				return fmt.Errorf("load configuration: %w", err)
			}
			if err := cmd.Help(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "\nConfigured destination: %s\n", cfg.MountRoot)
			return nil
		},
		Args: cobra.NoArgs,
	}
	root.PersistentFlags().StringVar(&configPath, "config", config.DefaultPath, "Configuration file (no directory search)")
	var mountRoot string
	var yes bool
	setup := &cobra.Command{Use: "setup", Short: "Configure your mount destination", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return runSetup(cmd, ops, configPath, mountRoot, yes) },
	}
	setup.Flags().StringVar(&mountRoot, "mount-root", "", "Absolute directory where your VM filesystems appear")
	setup.Flags().BoolVar(&yes, "yes", false, "Confirm saving and creation of a missing mount directory")
	root.AddCommand(setup)
	root.AddCommand(&cobra.Command{Use: "config", Short: "Show configuration and its location", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := ops.load(configPath)
			if err != nil {
				return fmt.Errorf("load %s: %w", configPath, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Configuration: %s\nmount_root: %s\n", configPath, cfg.MountRoot)
			return nil
		},
	})
	root.AddCommand(&cobra.Command{Use: "doctor", Short: "Check your host runtime dependencies", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := ops.doctor(cmd.Context()); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Your host runtime checks passed.")
			return nil
		},
	})
	var readOnly bool
	mount := vmCommand("mount", "Mount your guest filesystems (writable by default)", func(cmd *cobra.Command, id int) error {
		if _, err := requireConfig(cmd, ops, configPath); err != nil {
			return err
		}
		if err := ops.mount(cmd.Context(), id, configPath, readOnly); err != nil {
			return err
		}
		state, err := ops.status(id)
		if err != nil {
			return fmt.Errorf("your mount started, but its recorded location could not be read: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Your guest root is available at %s\n", state.Public)
		return nil
	})
	mount.Flags().BoolVar(&readOnly, "ro", false, "Mount all guest disks read-only")
	root.AddCommand(mount)
	root.AddCommand(vmCommand("status", "Show your recorded repair session", func(cmd *cobra.Command, id int) error {
		state, err := ops.status(id)
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(state)
	}))
	for _, action := range []struct {
		name, description string
		run               func(context.Context, int) error
	}{
		{"unmount", "Safely finalize and unmount your guest", ops.unmount},
		{"recover", "Reconcile an interrupted repair session", ops.recover},
	} {
		root.AddCommand(vmCommand(action.name, action.description, func(cmd *cobra.Command, id int) error {
			if err := action.run(cmd.Context(), id); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "You can now run: qm start %d\n", id)
			return nil
		}))
	}
	root.AddCommand(vmCommand("reset-root", "Set your unmounted guest's root password", func(cmd *cobra.Command, id int) error {
		if _, err := requireConfig(cmd, ops, configPath); err != nil {
			return err
		}
		password, err := ops.password(cmd)
		if err != nil {
			return err
		}
		defer clear(password)
		if err := ops.reset(cmd.Context(), id, configPath, password); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Your guest root password was updated.\nYou can now run: qm start %d\n", id)
		return nil
	}))
	worker := vmCommand("_worker", "Internal detached session worker", func(cmd *cobra.Command, id int) error { return ops.worker(cmd.Context(), id) })
	worker.Hidden = true
	root.AddCommand(worker)
	return root
}

func vmCommand(name, description string, run func(*cobra.Command, int) error) *cobra.Command {
	return &cobra.Command{Use: name + " VMID", Short: description, Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.Atoi(args[0])
			if err != nil || id < 100 || id > 999999999 {
				return errors.New("your VMID must be an integer from 100 to 999999999")
			}
			return run(cmd, id)
		},
	}
}

func requireConfig(cmd *cobra.Command, ops operations, path string) (config.Config, error) {
	cfg, err := ops.load(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, missingConfig(cmd, path)
	}
	if err != nil {
		return cfg, fmt.Errorf("load configuration: %w", err)
	}
	return cfg, nil
}

func missingConfig(cmd *cobra.Command, path string) error {
	fmt.Fprintf(cmd.ErrOrStderr(), "Configure your mount destination first:\n  pvedisk --config %q setup --mount-root /your/mount/directory --yes\n", path)
	return errors.New("your configuration is missing")
}

func isTerminal(cmd *cobra.Command) bool {
	in, inOK := cmd.InOrStdin().(*os.File)
	out, outOK := cmd.OutOrStdout().(*os.File)
	return inOK && outOK && term.IsTerminal(int(in.Fd())) && term.IsTerminal(int(out.Fd()))
}

func readPassword(cmd *cobra.Command) ([]byte, error) {
	input, ok := cmd.InOrStdin().(*os.File)
	if !ok || !term.IsTerminal(int(input.Fd())) {
		return nil, errors.New("you must use a terminal to enter your password")
	}
	fmt.Fprint(cmd.ErrOrStderr(), "Your new root password: ")
	first, err := term.ReadPassword(int(input.Fd()))
	fmt.Fprintln(cmd.ErrOrStderr())
	if err != nil {
		clear(first)
		return nil, fmt.Errorf("read your password: %w", err)
	}
	fmt.Fprint(cmd.ErrOrStderr(), "Enter your password again: ")
	second, err := term.ReadPassword(int(input.Fd()))
	fmt.Fprintln(cmd.ErrOrStderr())
	defer clear(second)
	if err != nil {
		clear(first)
		return nil, fmt.Errorf("confirm your password: %w", err)
	}
	if len(first) == 0 || !bytes.Equal(first, second) {
		clear(first)
		return nil, errors.New("your passwords must match and cannot be empty")
	}
	if bytes.ContainsAny(first, "\r\n\x00") {
		clear(first)
		return nil, errors.New("your password cannot contain line breaks or a zero byte")
	}
	return first, nil
}

// Keep the setup write behind the session lock so mount cannot start between the
// unresolved-session check and the configuration replacement.
func runSetup(cmd *cobra.Command, ops operations, path, requested string, yes bool) error {
	if yes && requested == "" {
		return errors.New("you must provide --mount-root when using --yes")
	}
	var canonical string
	if yes {
		var err error
		canonical, _, err = config.ValidateRoot(requested)
		if err != nil {
			return err
		}
		fmt.Fprint(cmd.OutOrStdout(), layout(canonical))
	} else {
		if !ops.terminal(cmd) {
			return errors.New("interactive setup requires a terminal; use setup --mount-root /your/mount/directory --yes")
		}
		var confirmed bool
		var err error
		canonical, confirmed, err = wizard(cmd.InOrStdin(), cmd.OutOrStdout(), requested)
		if err != nil {
			return err
		}
		if !confirmed {
			fmt.Fprintln(cmd.OutOrStdout(), "Your setup was cancelled.")
			return nil
		}
	}
	err := ops.setupLock(func() error {
		active, err := ops.hasSessions()
		if err != nil {
			return fmt.Errorf("check repair sessions: %w", err)
		}
		if active {
			old, err := ops.load(path)
			if err != nil || old.MountRoot != canonical {
				return errors.New("you cannot change your destination while active or unresolved repair sessions exist")
			}
		}
		checked, exists, err := config.ValidateRoot(canonical)
		if err != nil {
			return err
		}
		if checked != canonical {
			return errors.New("your destination changed during setup; run setup again")
		}
		if !exists {
			if err := os.MkdirAll(canonical, 0750); err != nil {
				return fmt.Errorf("create your mount destination: %w", err)
			}
			checked, _, err = config.ValidateRoot(canonical)
			if err != nil {
				return err
			}
			if checked != canonical {
				return errors.New("your destination changed during creation; run setup again")
			}
		}
		return ops.save(path, config.Config{MountRoot: canonical})
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Your configuration was saved to %s\n", path)
	return nil
}

func layout(root string) string {
	return fmt.Sprintf("Your guest root: %s/vm400/\nYour additional filesystems: %s/vm400-extra/<filesystem-id>/\n", root, root)
}
