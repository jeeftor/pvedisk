package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/jeeftor/pvedisk/internal/password"
	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

type utilityOperations struct {
	prompt func(*cobra.Command, bool) ([]byte, error)
	hash   func([]byte) (string, error)
	verify func([]byte, string) (bool, error)
}

func utilsCommand() *cobra.Command {
	command := newUtilsCommand(utilityOperations{
		prompt: readUtilityPassword,
		hash:   password.Hash, verify: password.Verify,
	})
	command.AddCommand(selinuxContextCommand(readSELinuxContext))
	return command
}

func newUtilsCommand(ops utilityOperations) *cobra.Command {
	utilities := &cobra.Command{Use: "utils", Short: "Optional repair and password helpers", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error { return cmd.Help() }}
	utilities.AddCommand(&cobra.Command{
		Use: "hash-password", Short: "Generate a Linux yescrypt hash for your password",
		Long: "Enter your password twice without echo. Your sensitive password hash is printed alone on standard output. This command does not change any guest files.",
		Args: noPasswordArguments,
		RunE: func(cmd *cobra.Command, args []string) error {
			secret, err := ops.prompt(cmd, true)
			defer clear(secret)
			if err != nil {
				return err
			}
			encoded, err := ops.hash(secret)
			if err != nil {
				return fmt.Errorf("generate your password hash: %w", err)
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), encoded)
			return err
		},
	})
	var shadowPath, username string
	verify := &cobra.Command{
		Use: "verify-password", Short: "Check your known password against one shadow entry",
		Long: "Read one account from your shadow file and prompt once for your known password without echo. This command reports a match or a mismatch; it does not change files or operate on a VM.",
		Args: noPasswordArguments,
		RunE: func(cmd *cobra.Command, args []string) error {
			encoded, err := readShadowHash(shadowPath, username)
			if err != nil {
				return err
			}
			secret, err := ops.prompt(cmd, false)
			defer clear(secret)
			if err != nil {
				return err
			}
			matches, err := ops.verify(secret, encoded)
			if err != nil {
				return fmt.Errorf("verify your password: %w", err)
			}
			if !matches {
				return errors.New("your password does not match the selected shadow entry")
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), outputPalette(cmd.OutOrStdout()).success.Render(fmt.Sprintf("Your password matches the shadow entry for %s.", username)))
			return err
		},
	}
	verify.Flags().StringVar(&shadowPath, "shadow", "", "Absolute path to your shadow file (required)")
	verify.Flags().StringVar(&username, "user", "root", "Account whose known password you want to verify")
	utilities.AddCommand(verify)
	return utilities
}

func noPasswordArguments(cmd *cobra.Command, args []string) error {
	if len(args) != 0 {
		return errors.New("enter your password at the hidden terminal prompt; positional arguments are not accepted")
	}
	return nil
}

const maxShadowSize = 1024 * 1024

func readShadowHash(path, username string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("provide an absolute shadow file path with --shadow")
	}
	if username == "" || strings.ContainsRune(username, ':') || strings.ContainsFunc(username, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return "", errors.New("your account name must not be empty or contain whitespace, control characters, or a colon")
	}
	file, err := openShadow(path)
	if err != nil {
		return "", fmt.Errorf("open your shadow file: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxShadowSize+1))
	if err != nil {
		return "", fmt.Errorf("read your shadow file: %w", err)
	}
	defer clear(data)
	if len(data) > maxShadowSize {
		return "", errors.New("your shadow file exceeds the one-megabyte limit")
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "", errors.New("your shadow file contains a zero byte")
	}
	var selected string
	found := false
	accounts := make(map[string]bool)
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) != 9 || fields[0] == "" || strings.ContainsFunc(fields[0], func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
			return "", errors.New("your shadow file contains a malformed entry")
		}
		if accounts[fields[0]] {
			return "", errors.New("your shadow file contains duplicate account entries")
		}
		accounts[fields[0]] = true
		if fields[0] == username {
			selected = fields[1]
			found = true
		}
	}
	if !found {
		return "", errors.New("your selected account has no shadow entry")
	}
	if selected == "" {
		return "", errors.New("your selected account has no password hash")
	}
	if strings.HasPrefix(selected, "!") || strings.HasPrefix(selected, "*") {
		return "", errors.New("your selected account has a locked password")
	}
	return selected, nil
}

// Open every component without following symbolic links, including directories
// inside a mounted guest that could otherwise redirect a lookup onto the host.
func openShadow(path string) (*os.File, error) {
	parts := strings.Split(strings.TrimPrefix(filepath.Clean(path), "/"), "/")
	directory, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	for index, part := range parts {
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
		if index < len(parts)-1 {
			flags |= unix.O_DIRECTORY
		}
		next, openErr := unix.Openat(directory, part, flags, 0)
		unix.Close(directory)
		if openErr != nil {
			return nil, openErr
		}
		directory = next
	}
	file := os.NewFile(uintptr(directory), path)
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, errors.New("your shadow path must name a regular file without symbolic links")
	}
	return file, nil
}

func readSecret(cmd *cobra.Command, prompt string) ([]byte, error) {
	input, ok := cmd.InOrStdin().(*os.File)
	if !ok || !term.IsTerminal(int(input.Fd())) {
		return nil, errors.New("you must use a terminal to enter your password")
	}
	fmt.Fprint(cmd.ErrOrStderr(), outputPalette(cmd.ErrOrStderr()).label.Render(prompt))
	secret, err := term.ReadPassword(int(input.Fd()))
	fmt.Fprintln(cmd.ErrOrStderr())
	if err != nil {
		clear(secret)
		return nil, fmt.Errorf("read your password: %w", err)
	}
	if len(secret) == 0 || bytes.ContainsAny(secret, "\r\n\x00") {
		clear(secret)
		return nil, errors.New("your password must not be empty or contain line breaks or a zero byte")
	}
	return secret, nil
}

func readConfirmedPassword(cmd *cobra.Command, prompt string) ([]byte, error) {
	first, err := readSecret(cmd, prompt)
	if err != nil {
		return nil, err
	}
	second, err := readSecret(cmd, "Enter your password again: ")
	defer clear(second)
	if err != nil {
		clear(first)
		return nil, err
	}
	if !bytes.Equal(first, second) {
		clear(first)
		return nil, errors.New("your passwords must match")
	}
	return first, nil
}
