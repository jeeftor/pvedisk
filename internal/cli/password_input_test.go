package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestUtilityPasswordStdin(t *testing.T) {
	for _, test := range []struct {
		name, input, want string
	}{
		{"end of file", "private", "private"},
		{"newline", "private\n", "private"},
		{"carriage return newline", "private\r\n", "private"},
		{"preserve spaces", "  privé%value \t \n", "  privé%value \t "},
		{"space password", " \n", " "},
		{"raw bytes", string([]byte{0xff, 0xfe, '%', '\n'}), string([]byte{0xff, 0xfe, '%'})},
		{"maximum", strings.Repeat("x", maxUtilityPasswordSize), strings.Repeat("x", maxUtilityPasswordSize)},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, confirm := range []bool{true, false} {
				var stdout, stderr bytes.Buffer
				cmd := &cobra.Command{}
				cmd.SetIn(strings.NewReader(test.input))
				cmd.SetOut(&stdout)
				cmd.SetErr(&stderr)
				secret, err := readUtilityPassword(cmd, confirm)
				if err != nil || string(secret) != test.want {
					t.Fatalf("input result = %q, %v", secret, err)
				}
				clear(secret)
				if stdout.Len() != 0 || stderr.Len() != 0 {
					t.Fatal("piped password input emitted a prompt or password")
				}
			}
		})
	}
}

type retainedPasswordReader struct {
	input    []byte
	retained []byte
	err      error
	count    int
}

func (r *retainedPasswordReader) Read(p []byte) (int, error) {
	r.retained = p
	n := copy(p, r.input)
	r.count += n
	r.input = r.input[n:]
	if r.err != nil {
		return n, r.err
	}
	return n, io.EOF
}

func TestUtilityPasswordRejectsAndClearsInput(t *testing.T) {
	for _, test := range []struct {
		name, input string
		readError   error
	}{
		{"empty", "", nil},
		{"newline only", "\n", nil},
		{"carriage return newline only", "\r\n", nil},
		{"multiple lines", "do-not-leak\nsecond\n", nil},
		{"two trailing newlines", "do-not-leak\n\n", nil},
		{"bare carriage return", "do-not-leak\r", nil},
		{"zero byte", "do-not-leak\x00", nil},
		{"too long", strings.Repeat("x", maxUtilityPasswordSize+10), nil},
		{"partial read error", "do-not-leak", errors.New("reader error containing do-not-leak")},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := &retainedPasswordReader{input: []byte(test.input), err: test.readError}
			var stdout, stderr bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetIn(reader)
			cmd.SetOut(&stdout)
			cmd.SetErr(&stderr)
			secret, err := readUtilityPassword(cmd, true)
			if err == nil || secret != nil {
				t.Fatal("accepted invalid password input")
			}
			if strings.Contains(err.Error(), "do-not-leak") || stdout.Len() != 0 || stderr.Len() != 0 {
				t.Fatal("invalid input leaked sensitive data or emitted a prompt")
			}
			for _, b := range reader.retained {
				if b != 0 {
					t.Fatal("failed input left uncleared password bytes")
				}
			}
			if reader.count > maxUtilityPasswordSize+1 {
				t.Fatal("read beyond bounded password input")
			}
		})
	}
}

func TestUtilityPasswordRedirectedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(path, []byte("my-private-value\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	cmd := &cobra.Command{}
	cmd.SetIn(input)
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	secret, err := readUtilityPassword(cmd, true)
	defer clear(secret)
	if err != nil || string(secret) != "my-private-value" || output.Len() != 0 {
		t.Fatalf("redirected password result incorrect: %v", err)
	}
}

func TestUtilityCommandsReadPipedPassword(t *testing.T) {
	const secret = " leading privé%value "
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	shadow := filepath.Join(base, "shadow")
	if err := os.WriteFile(shadow, []byte("root:$y$fixture:1:0:99999:7:::\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"hash-password"},
		{"verify-password", "--shadow", shadow},
	} {
		t.Run(args[0], func(t *testing.T) {
			var retained []byte
			ops := utilityOperations{
				prompt: readUtilityPassword,
				hash: func(password []byte) (string, error) {
					retained = password
					if string(password) != secret {
						t.Fatal("hash command changed password bytes")
					}
					return "$y$fixture", nil
				},
				verify: func(password []byte, encoded string) (bool, error) {
					retained = password
					if string(password) != secret || encoded != "$y$fixture" {
						t.Fatal("verify command changed password or hash")
					}
					return true, nil
				},
			}
			var stdout, stderr bytes.Buffer
			cmd := newUtilsCommand(ops)
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
			cmd.SetArgs(args)
			cmd.SetIn(strings.NewReader(secret + "\n"))
			cmd.SetOut(&stdout)
			cmd.SetErr(&stderr)
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(stdout.Bytes(), []byte(secret)) || stderr.Len() != 0 {
				t.Fatal("utility emitted plaintext or a terminal prompt")
			}
			if args[0] == "hash-password" && stdout.String() != "$y$fixture\n" {
				t.Fatal("hash command did not print only its encoded result")
			}
			for _, b := range retained {
				if b != 0 {
					t.Fatal("utility command did not clear password after use")
				}
			}
		})
	}
}

func TestUtilityCommandsRejectInvalidPipedPassword(t *testing.T) {
	const secret = "do-not-leak"
	var output bytes.Buffer
	cmd := newUtilsCommand(utilityOperations{
		prompt: readUtilityPassword,
		hash: func([]byte) (string, error) {
			t.Fatal("invalid input reached password hashing")
			return "", nil
		},
	})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"hash-password"})
	cmd.SetIn(strings.NewReader(secret + "\nsecond\n"))
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	err := cmd.Execute()
	if err == nil || strings.Contains(err.Error(), secret) || output.Len() != 0 {
		t.Fatalf("invalid piped input was accepted or emitted data: %v", err)
	}
}
