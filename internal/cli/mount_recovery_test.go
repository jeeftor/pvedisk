package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jeeftor/pvedisk/internal/session"
	"github.com/spf13/cobra"
)

func TestMountOffersRecoveryOnlyForExistingSessionsAtATerminal(t *testing.T) {
	busy := errors.New("your disk is busy; protection is retained")
	for _, tc := range []struct {
		name, input                             string
		terminal                                bool
		mountErr, recoverErr, retryErr, wantErr error
		wantRecover                             bool
	}{
		{name: "yes", input: "y\n", terminal: true, mountErr: session.ErrSessionExists, wantRecover: true},
		{name: "yes word", input: "YES\n", terminal: true, mountErr: fmt.Errorf("saved session: %w", session.ErrSessionExists), wantRecover: true},
		{name: "default no", input: "\n", terminal: true, mountErr: session.ErrSessionExists, wantErr: session.ErrSessionExists},
		{name: "decline", input: "n\n", terminal: true, mountErr: session.ErrSessionExists, wantErr: session.ErrSessionExists},
		{name: "end of input", terminal: true, mountErr: session.ErrSessionExists, wantErr: session.ErrSessionExists},
		{name: "incomplete answer", input: "yes", terminal: true, mountErr: session.ErrSessionExists, wantErr: session.ErrSessionExists},
		{name: "noninteractive", input: "y\n", mountErr: session.ErrSessionExists, wantErr: session.ErrSessionExists},
		{name: "unrelated error", input: "y\n", terminal: true, mountErr: os.ErrPermission, wantErr: os.ErrPermission},
		{name: "recovery blocked", input: "y\n", terminal: true, mountErr: session.ErrSessionExists, recoverErr: busy, wantRecover: true, wantErr: busy},
		{name: "retry blocked", input: "y\n", terminal: true, mountErr: session.ErrSessionExists, retryErr: busy, wantRecover: true, wantErr: busy},
		{name: "new conflict does not loop", input: "y\ny\n", terminal: true, mountErr: session.ErrSessionExists, retryErr: session.ErrSessionExists, wantRecover: true, wantErr: session.ErrSessionExists},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ops := pickerOperations()
			ops.terminal = func(*cobra.Command) bool { return tc.terminal }
			var calls []string
			ops.mount = func(_ context.Context, id int, path string, ro bool, _ func(string)) error {
				if id != 408 || path != "/custom.yaml" || !ro {
					t.Fatalf("mount arguments changed: id=%d path=%q ro=%v", id, path, ro)
				}
				calls = append(calls, "mount")
				if len(calls) == 1 {
					return tc.mountErr
				}
				return tc.retryErr
			}
			ops.recover = func(_ context.Context, id int) error {
				if id != 408 {
					t.Fatalf("recovered a different VM: %d", id)
				}
				calls = append(calls, "recover")
				return tc.recoverErr
			}
			ops.status = func(int) (*session.State, error) { return &session.State{Public: "/repairs/vm408"}, nil }
			output, err := executeInput(ops, tc.input, "--config", "/custom.yaml", "mount", "408", "--ro")
			if (tc.wantErr == nil && err != nil) || (tc.wantErr != nil && !errors.Is(err, tc.wantErr)) {
				t.Fatalf("output=%q err=%v, want %v", output, err, tc.wantErr)
			}
			wantCalls := "mount"
			if tc.wantRecover {
				wantCalls += ",recover"
				if tc.recoverErr == nil {
					wantCalls += ",mount"
				}
			}
			if strings.Join(calls, ",") != wantCalls {
				t.Fatalf("operations=%v, want %s", calls, wantCalls)
			}
			wantPrompt := tc.terminal && errors.Is(tc.mountErr, session.ErrSessionExists)
			if strings.Contains(output, "[y/N]") != wantPrompt || strings.Count(output, "[y/N]") > 1 {
				t.Fatalf("unexpected recovery prompt: %q", output)
			}
		})
	}
}

func TestCanceledMountDoesNotOfferRecovery(t *testing.T) {
	ops := pickerOperations()
	ops.mount = func(context.Context, int, string, bool, func(string)) error { return session.ErrSessionExists }
	ops.recover = func(context.Context, int) error { t.Fatal("canceled command recovered a session"); return nil }
	command := newCommand(ops)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	command.SetContext(ctx)
	command.SetIn(strings.NewReader("y\n"))
	command.SetArgs([]string{"mount", "408"})
	if err := command.Execute(); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled mount lost cancellation: %v", err)
	}
}
