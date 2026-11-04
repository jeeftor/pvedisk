// Package cli provides the pvedisk command line and first-run setup wizard.
package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jeeftor/pvedisk/internal/buildinfo"
	"github.com/jeeftor/pvedisk/internal/config"
	"github.com/jeeftor/pvedisk/internal/proxmox"
	"github.com/jeeftor/pvedisk/internal/session"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

type operations struct {
	hasSessions  func() (bool, error)
	setupLock    func(func() error) error
	mount        func(context.Context, int, string, bool, func(string)) error
	unmount      func(context.Context, int) error
	recover      func(context.Context, int) error
	reset        func(context.Context, int, string, []byte) error
	worker       func(context.Context, int) error
	status       func(int) (*session.State, error)
	describe     func(int) (*session.Description, error)
	listSessions func() ([]session.Description, error)
	listVMs      func(context.Context) ([]proxmox.VM, error)
	doctor       func(context.Context) error
	load         func(string) (config.Config, error)
	save         func(string, config.Config) error
	terminal     func(*cobra.Command) bool
	password     func(*cobra.Command) ([]byte, error)
}

// NewCommand constructs the CLI. Configuration is read only from the explicit
// path, or /etc/pvedisk/config.yaml, and never from the working directory.
func NewCommand() *cobra.Command {
	m := session.New()
	return newCommand(operations{
		hasSessions: m.HasSessions,
		setupLock:   m.WithSetupLock,
		mount:       m.Mount, unmount: m.Unmount, recover: m.Recover,
		reset: m.ResetRoot, worker: m.Worker,
		status:   m.Status,
		describe: m.Describe, listSessions: m.List, listVMs: proxmox.New().List,
		doctor: proxmox.New().Doctor,
		load:   config.Load, save: config.Save,
		terminal: isTerminal, password: readPassword,
	})
}

func newCommand(ops operations) *cobra.Command {
	var configPath string
	root := &cobra.Command{
		Use: "pvedisk", Short: "Mount and repair stopped Proxmox Linux guests",
		Version:      buildinfo.Version,
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
			p := outputPalette(cmd.OutOrStdout())
			fmt.Fprintf(cmd.OutOrStdout(), "\n%s %s\n", p.label.Render("Configured destination:"), p.path.Render(cfg.MountRoot))
			return nil
		},
		Args: cobra.NoArgs,
	}
	root.SetVersionTemplate("{{.Version}}\n")
	defaultHelp := root.HelpFunc()
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		if !colorEnabled(cmd.OutOrStdout()) {
			defaultHelp(cmd, args)
			return
		}
		var output bytes.Buffer
		original := cmd.OutOrStdout()
		cmd.SetOut(&output)
		defaultHelp(cmd, args)
		cmd.SetOut(original)
		fmt.Fprint(original, styledHelp(original, output.String()))
	})
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
			p := outputPalette(cmd.OutOrStdout())
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n%s %s\n", p.label.Render("Configuration:"), p.path.Render(configPath), p.label.Render("mount_root:"), p.path.Render(cfg.MountRoot))
			return nil
		},
	})
	root.AddCommand(&cobra.Command{Use: "doctor", Short: "Check your host runtime dependencies", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := ops.doctor(cmd.Context()); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), outputPalette(cmd.OutOrStdout()).success.Render("Your host runtime checks passed."))
			return nil
		},
	})
	var readOnly bool
	mount := vmCommand("mount", "Mount your guest filesystems (writable by default)", func(cmd *cobra.Command, id int) error {
		if _, err := requireConfig(cmd, ops, configPath); err != nil {
			return err
		}
		started := time.Now()
		if err := ops.mount(cmd.Context(), id, configPath, readOnly, func(phase string) {
			if message := mountPhaseMessage(phase); message != "" {
				p := outputPalette(cmd.OutOrStdout())
				fmt.Fprintln(cmd.OutOrStdout(), mountProgress(p, id, phase, message, time.Since(started)))
			}
		}); err != nil {
			return fmt.Errorf("mount VM %d: %w\nCheck your session with: pvedisk status %d", id, err, id)
		}
		state, err := ops.status(id)
		if err != nil {
			return fmt.Errorf("your mount started, but its recorded location could not be read: %w", err)
		}
		mode := "read/write"
		if state.ReadOnly {
			mode = "read-only"
		}
		p := outputPalette(cmd.OutOrStdout())
		modeStyle := p.warning
		if state.ReadOnly {
			modeStyle = p.success
		}
		result := fmt.Sprintf("%s (%s).\n%s %s\n%s %s", p.success.Render(fmt.Sprintf("Your VM %d is mounted", id)), modeStyle.Render(mode), p.label.Render("Guest root:"), p.path.Render(state.Public), p.label.Render("Additional filesystems:"), p.path.Render(state.Extras))
		fmt.Fprintln(cmd.OutOrStdout(), p.panel(result, terminalWidth(cmd.OutOrStdout())))
		if len(state.Inventory.Filesystems) > 0 {
			var table bytes.Buffer
			w := tabwriter.NewWriter(&table, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "DEVICE\tTYPE\tGUEST MOUNTPOINT")
			for _, fs := range state.Inventory.Filesystems {
				point := fs.Mountpoint
				if !fs.Mounted {
					point = "not mounted"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", fs.Device, fs.Type, point)
			}
			if err := w.Flush(); err != nil {
				return err
			}
			header, rows, _ := strings.Cut(table.String(), "\n")
			fmt.Fprintln(cmd.OutOrStdout(), p.label.Render(header))
			fmt.Fprint(cmd.OutOrStdout(), rows)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", p.muted.Render("When you finish, run:"), p.path.Render(fmt.Sprintf("pvedisk unmount %d", id)))
		return nil
	})
	mount.Flags().BoolVar(&readOnly, "ro", false, "Mount all guest disks read-only")
	mount.Use = "mount [VMID]"
	mount.Args = cobra.MaximumNArgs(1)
	runMount := mount.RunE
	mount.RunE = func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			if _, err := requireConfig(cmd, ops, configPath); err != nil {
				return err
			}
			if !ops.terminal(cmd) {
				return errors.New("provide your VM ID when running without a terminal: pvedisk mount 400")
			}
			id, err := selectMountVM(cmd, ops)
			if err != nil {
				return err
			}
			args = []string{strconv.Itoa(id)}
		}
		return runMount(cmd, args)
	}
	root.AddCommand(mount)
	status := vmCommand("status", "Show your mounted disks and sessions awaiting cleanup", func(cmd *cobra.Command, id int) error {
		description, err := ops.describe(id)
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("your VM %d has no recorded pvedisk repair session. Mounts created by other tools are not tracked. To start a session, run: pvedisk mount %d", id, id)
		}
		if err != nil {
			return fmt.Errorf("read your repair session for VM %d: %w", id, err)
		}
		return writeStatus(cmd.OutOrStdout(), description)
	})
	status.Long = "Show your mounted disks and sessions awaiting cleanup. A saved session can remain after its mounts disappear. With a VMID, print JSON containing your saved session and checks of its current mounts and worker. The summary checks VM availability against this node only."
	var statusJSON bool
	status.Flags().BoolVar(&statusJSON, "json", false, "Print your session summary as JSON")
	status.Use = "status [VMID]"
	status.Args = cobra.MaximumNArgs(1)
	runStatus := status.RunE
	status.RunE = func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			return runStatus(cmd, args)
		}
		sessions, err := ops.listSessions()
		if err != nil {
			return fmt.Errorf("list your repair sessions: %w", err)
		}
		if statusJSON {
			return writeStatus(cmd.OutOrStdout(), sessions)
		}
		vms, inventoryErr := ops.listVMs(cmd.Context())
		return printSessions(cmd.OutOrStdout(), sessions, vms, inventoryErr)
	}
	root.AddCommand(status)
	for _, action := range []struct {
		name, description string
		run               func(context.Context, int) error
	}{
		{"unmount", "Safely finalize and unmount your guest", ops.unmount},
		{"recover", "Reconcile an interrupted repair session", ops.recover},
	} {
		command := vmCommand(action.name, action.description, func(cmd *cobra.Command, id int) error {
			if err := action.run(cmd.Context(), id); err != nil {
				return err
			}
			p := outputPalette(cmd.OutOrStdout())
			fmt.Fprintln(cmd.OutOrStdout(), p.success.Render(fmt.Sprintf("Your cleanup for VM %d is complete.", id)))
			if action.name == "unmount" {
				vms, err := ops.listVMs(cmd.Context())
				if err == nil {
					for _, vm := range vms {
						if vm.ID == id {
							fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", p.success.Render("You can now run:"), p.path.Render(fmt.Sprintf("qm start %d", id)))
							break
						}
					}
				}
			}
			return nil
		})
		if action.name == "unmount" {
			command.Use = "unmount [VMID]"
			command.Args = cobra.MaximumNArgs(1)
			runUnmount := command.RunE
			command.RunE = func(cmd *cobra.Command, args []string) error {
				if len(args) == 0 {
					if !ops.terminal(cmd) {
						return errors.New("provide your VM ID when running without a terminal: pvedisk unmount 400")
					}
					id, err := selectUnmountVM(cmd, ops)
					if err != nil {
						return err
					}
					args = []string{strconv.Itoa(id)}
				}
				return runUnmount(cmd, args)
			}
		}
		root.AddCommand(command)
	}
	utilities := utilsCommand()
	resetRoot := vmCommand("reset-root", "Set a supported Linux guest's root password", func(cmd *cobra.Command, id int) error {
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
		p := outputPalette(cmd.OutOrStdout())
		fmt.Fprintf(cmd.OutOrStdout(), "%s\n%s %s\n", p.success.Render("Your guest root password was updated."), p.success.Render("You can now run:"), p.path.Render(fmt.Sprintf("qm start %d", id)))
		return nil
	})
	resetRoot.Long = "Set the root password of a supported Linux guest through virt-customize; this helper is not limited to RHEL 10. You need valid configuration and unmounted guest disks. Enter your new password twice at a terminal. Your guest is never restarted automatically."
	utilities.AddCommand(resetRoot)
	worker := vmCommand("_worker", "Internal detached session worker", func(cmd *cobra.Command, id int) error { return ops.worker(cmd.Context(), id) })
	worker.Hidden = true
	root.AddCommand(worker)
	root.AddCommand(utilities)
	root.AddCommand(versionCommand())
	return root
}

func mountPhaseMessage(phase string) string {
	switch phase {
	case "pending":
		return "starting your repair worker"
	case "inspecting":
		return "checking your VM and attached disks"
	case "stopping":
		return "ensuring your VM is stopped; graceful shutdown can take up to 120 seconds"
	case "acquiring":
		return "acquiring startup protection and rechecking your disks"
	case "protected":
		return "startup protection acquired; checking for other disk users"
	case "launching":
		return "starting your libguestfs appliance"
	case "mounting":
		return "inspecting and mounting your guest filesystems"
	case "prepared":
		return "exposing your guest filesystem tree"
	default:
		return ""
	}
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
	return readConfirmedPassword(cmd, "Your new root password: ")
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
			fmt.Fprintln(cmd.OutOrStdout(), outputPalette(cmd.OutOrStdout()).muted.Render("Your setup was cancelled."))
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
	p := outputPalette(cmd.OutOrStdout())
	fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", p.success.Render("Your configuration was saved to"), p.path.Render(path))
	return nil
}

func layout(root string) string {
	return fmt.Sprintf("Your guest root: %s/vm400/\nYour additional filesystems: %s/vm400-extra/<filesystem-id>/\n", root, root)
}
