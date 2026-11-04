package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/jeeftor/pvedisk/internal/config"
	"github.com/jeeftor/pvedisk/internal/mcpserver"
)

func TestMCPPassesListenerAuthAndParsedConfiguration(t *testing.T) {
	for _, test := range []struct {
		name    string
		args    []string
		path    string
		options mcpserver.HTTPOptions
	}{
		{"defaults", nil, config.DefaultPath, mcpserver.HTTPOptions{Listen: "127.0.0.1:8766"}},
		{"airgap-lan", []string{"--listen", "0.0.0.0:8766", "--no-token", "--config", "/site/pvedisk.yaml"}, "/site/pvedisk.yaml", mcpserver.HTTPOptions{Listen: "0.0.0.0:8766", NoToken: true}},
		{"token", []string{"--listen", "10.0.0.8:8766", "--token-file", "/etc/pvedisk/mcp.token"}, config.DefaultPath, mcpserver.HTTPOptions{Listen: "10.0.0.8:8766", TokenFile: "/etc/pvedisk/mcp.token"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ops := testOperations()
			ops.load = func(string) (config.Config, error) {
				t.Fatal("MCP startup loaded configuration")
				return config.Config{}, nil
			}
			ops.doctor = func(context.Context) error { t.Fatal("MCP startup ran doctor"); return nil }
			called := false
			ops.mcp = func(ctx context.Context, path string, options mcpserver.HTTPOptions, output io.Writer) error {
				called = true
				if ctx == nil || path != test.path || options != test.options {
					t.Fatalf("path=%q options=%+v", path, options)
				}
				_, err := io.WriteString(output, "MCP listening\n")
				return err
			}
			command := newCommand(ops)
			var stdout, stderr bytes.Buffer
			command.SetOut(&stdout)
			command.SetErr(&stderr)
			command.SetArgs(append([]string{"mcp"}, test.args...))
			err := command.Execute()
			if err != nil || !called || stdout.Len() != 0 || stderr.String() != "MCP listening\n" {
				t.Fatalf("called=%v stdout=%q stderr=%q err=%v", called, stdout.String(), stderr.String(), err)
			}
		})
	}
}

func TestMCPRejectsConflictingAuthFlagsBeforeServing(t *testing.T) {
	ops := testOperations()
	ops.mcp = func(context.Context, string, mcpserver.HTTPOptions, io.Writer) error {
		t.Fatal("served with conflicting authentication flags")
		return nil
	}
	if _, err := execute(ops, "mcp", "--token-file", "/private/token", "--no-token"); err == nil {
		t.Fatal("conflicting authentication flags were accepted")
	}
}

func TestMCPPreservesBackendErrors(t *testing.T) {
	ops := testOperations()
	failure := errors.New("your listener requires an explicit authentication mode")
	ops.mcp = func(context.Context, string, mcpserver.HTTPOptions, io.Writer) error { return failure }
	if _, err := execute(ops, "mcp", "--listen", "0.0.0.0:8766"); !errors.Is(err, failure) {
		t.Fatalf("backend error changed: %v", err)
	}
}

func TestMCPPropagatesCommandCancellation(t *testing.T) {
	ops := testOperations()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ops.mcp = func(serverContext context.Context, _ string, _ mcpserver.HTTPOptions, _ io.Writer) error {
		if !errors.Is(serverContext.Err(), context.Canceled) {
			t.Fatal("server context lost command cancellation")
		}
		return serverContext.Err()
	}
	command := newCommand(ops)
	command.SetArgs([]string{"mcp"})
	if err := command.ExecuteContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error changed: %v", err)
	}
}

func TestMCPRejectsPositionalArguments(t *testing.T) {
	ops := testOperations()
	ops.mcp = func(context.Context, string, mcpserver.HTTPOptions, io.Writer) error {
		t.Fatal("served with unexpected positional arguments")
		return nil
	}
	if _, err := execute(ops, "mcp", "400"); err == nil {
		t.Fatal("unexpected positional argument accepted")
	}
}
