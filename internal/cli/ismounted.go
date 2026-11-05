package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var errNotMounted = &exitError{code: 1}

type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string {
	if e.err == nil {
		return "your VM has no mounted pvedisk session"
	}
	return e.err.Error()
}

func (e *exitError) Unwrap() error { return e.err }

// ExitCode returns the command's process exit code, or zero for success.
// Commands without an explicit exit status retain the usual failure code of one.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var status *exitError
	if errors.As(err, &status) {
		return status.code
	}
	return 1
}

func isMountedCommand(ops operations) *cobra.Command {
	command := vmCommand("ismounted", "Check whether your pvedisk session is mounted (exit status only)", func(_ *cobra.Command, id int) error {
		description, err := ops.describe(id)
		if errors.Is(err, os.ErrNotExist) {
			return errNotMounted
		}
		if err != nil {
			return fmt.Errorf("inspect your pvedisk mounts for VM %d: %w", id, err)
		}
		if description == nil || description.State == nil {
			return fmt.Errorf("cannot determine your pvedisk mounts for VM %d: no valid session record", id)
		}
		o := description.Observation
		if description.Error == "" && o.Error == "" && o.MountsKnown && o.WorkerKnown {
			switch o.Status {
			case "mounted":
				if o.RootMounted && o.ExtrasMounted && o.StagingMounted && o.WorkerAlive {
					return nil
				}
			case "not-mounted":
				if !o.RootMounted && !o.ExtrasMounted && !o.StagingMounted {
					return errNotMounted
				}
			}
		}
		detail := description.Error
		if detail == "" {
			detail = o.Error
		}
		if detail == "" {
			detail = "your mounts or worker identity are incomplete or uncertain"
		}
		return fmt.Errorf("cannot confirm your pvedisk mounts for VM %d (status %q): %s; inspect: pvedisk status %d", id, o.Status, detail, id)
	})
	command.Long = "Check your recorded pvedisk session against current mounts and worker identity without changing state or loading site configuration. Exit 0: all recorded views are mounted and the worker is confirmed alive. Exit 1: no recorded session, or none of its recorded views remain mounted. Both results are silent. Exit 2: invalid arguments, failed inspection, partial mounts, or conflicting or uncertain state; a diagnostic is written to standard error. Only pvedisk sessions are tracked. Exit 1 does not prove cleanup is complete or your VM is safe to start."
	validateArgs, run := command.Args, command.RunE
	command.Args = func(cmd *cobra.Command, args []string) error {
		return mountProbeError(validateArgs(cmd, args))
	}
	command.RunE = func(cmd *cobra.Command, args []string) error {
		err := run(cmd, args)
		if errors.Is(err, errNotMounted) {
			return err
		}
		return mountProbeError(err)
	}
	command.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return mountProbeError(err) })
	return command
}

func mountProbeError(err error) error {
	if err == nil {
		return nil
	}
	return &exitError{code: 2, err: err}
}
