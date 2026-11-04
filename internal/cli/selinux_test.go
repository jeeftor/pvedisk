package cli

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jeeftor/pvedisk/internal/config"
	"github.com/spf13/cobra"
)

func TestSELinuxRelabelDispatchesWithExplicitConfiguration(t *testing.T) {
	ops := testOperations()
	path := "/site/pvedisk.yaml"
	ops.load = func(actual string) (config.Config, error) {
		if actual != path {
			t.Fatalf("wrong config path %q", actual)
		}
		return config.Config{MountRoot: "/repairs"}, nil
	}
	ops.password = func(*cobra.Command) ([]byte, error) { t.Fatal("SELinux relabel requested a password"); return nil, nil }
	ops.mount = func(context.Context, int, string, bool, func(string)) error {
		t.Fatal("SELinux helper invoked the mount command")
		return nil
	}
	called := false
	ops.scheduleSELinuxRelabel = func(_ context.Context, id int, actual string) error { called = id == 400 && actual == path; return nil }
	output, err := execute(ops, "utils", "selinux-relabel", "400", "--config", path)
	if err != nil || !called {
		t.Fatalf("called=%v output=%q err=%v", called, output, err)
	}
	for _, want := range []string{"scheduled for its next boot", "may take time during that boot", "has not been started", "qm start 400"} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing %q in %q", want, output)
		}
	}
	if strings.Contains(output, "\x1b") {
		t.Fatal("redirected SELinux output contains ANSI escapes")
	}
}

func TestSELinuxRelabelRequiresValidConfigurationBeforeMaintenance(t *testing.T) {
	for _, configErr := range []error{os.ErrNotExist, errors.New("damaged site configuration")} {
		ops := testOperations()
		ops.load = func(string) (config.Config, error) { return config.Config{}, configErr }
		ops.scheduleSELinuxRelabel = func(context.Context, int, string) error {
			t.Fatal("scheduled relabel without valid configuration")
			return nil
		}
		output, err := execute(ops, "utils", "selinux-relabel", "400")
		if !errors.Is(configErr, os.ErrNotExist) && !errors.Is(err, configErr) {
			t.Fatalf("configuration error lost: %v", err)
		}
		if err == nil || strings.Contains(output, "is scheduled") || strings.Contains(output, "qm start") {
			t.Fatalf("output=%q err=%v", output, err)
		}
		if errors.Is(configErr, os.ErrNotExist) && !strings.Contains(output, "setup --mount-root") {
			t.Fatalf("missing setup instructions: %q", output)
		}
	}
}

func TestSELinuxRelabelFailureDoesNotClaimSchedulingOrSuggestBoot(t *testing.T) {
	for _, failure := range []error{errors.New("your guest disks are already mounted"), errors.New("your guest has SELinux disabled"), errors.New("cleanup retained startup protection")} {
		ops := testOperations()
		ops.load = func(string) (config.Config, error) { return config.Config{MountRoot: "/repairs"}, nil }
		ops.scheduleSELinuxRelabel = func(context.Context, int, string) error { return failure }
		output, err := execute(ops, "utils", "selinux-relabel", "400")
		if !errors.Is(err, failure) || strings.Contains(output, "is scheduled") || strings.Contains(output, "qm start") {
			t.Fatalf("output=%q err=%v", output, err)
		}
	}
}

func TestSELinuxRelabelRejectsInvalidOrMissingVMID(t *testing.T) {
	ops := testOperations()
	ops.load = func(string) (config.Config, error) {
		t.Fatal("invalid VM identifier reached configuration loading")
		return config.Config{}, nil
	}
	ops.scheduleSELinuxRelabel = func(context.Context, int, string) error {
		t.Fatal("invalid VM identifier reached maintenance")
		return nil
	}
	for _, id := range []string{"99", "1000000000", "-1", "guest", "400;true"} {
		if _, err := execute(ops, "utils", "selinux-relabel", id); err == nil {
			t.Fatalf("accepted invalid VM identifier %q", id)
		}
	}
	if _, err := execute(ops, "utils", "selinux-relabel"); err == nil {
		t.Fatal("accepted missing VM identifier")
	}
}

func TestSELinuxRelabelIsAnOptionalUtilityAndHelpExplainsNextBoot(t *testing.T) {
	ops := testOperations()
	rootHelp, err := execute(ops, "--help")
	if err != nil || strings.Contains(rootHelp, "selinux-relabel") {
		t.Fatalf("relabel unexpectedly registered at root: %q err=%v", rootHelp, err)
	}
	utilityHelp, err := execute(ops, "utils", "--help")
	if err != nil || !strings.Contains(utilityHelp, "selinux-relabel") {
		t.Fatalf("missing utility registration: %q err=%v", utilityHelp, err)
	}
	help, err := execute(ops, "utils", "selinux-relabel", "--help")
	for _, want := range []string{"next boot", "/.autorelabel", "enforcing or permissive", "does not perform the relabel immediately", "restart your guest"} {
		if err != nil || !strings.Contains(help, want) {
			t.Fatalf("missing accurate help %q in %q err=%v", want, help, err)
		}
	}
}

func TestSELinuxRelabelPassesCancellationToMaintenance(t *testing.T) {
	ops := testOperations()
	ops.load = func(string) (config.Config, error) { return config.Config{MountRoot: "/repairs"}, nil }
	ops.scheduleSELinuxRelabel = func(ctx context.Context, _ int, _ string) error { return ctx.Err() }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	command := newCommand(ops)
	command.SetArgs([]string{"utils", "selinux-relabel", "400"})
	if err := command.ExecuteContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}
