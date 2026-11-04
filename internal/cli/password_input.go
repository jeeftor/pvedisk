package cli

import (
	"bytes"
	"errors"
	"io"
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

const maxUtilityPasswordSize = 64 * 1024

// Utility passwords come from a hidden terminal prompt or one bounded stdin
// value. Keep root-password repair on its separate terminal-only input path.
func readUtilityPassword(cmd *cobra.Command, confirm bool) ([]byte, error) {
	input := cmd.InOrStdin()
	if file, ok := input.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		if confirm {
			return readConfirmedPassword(cmd, "Your password: ")
		}
		return readSecret(cmd, "Your known password: ")
	}
	// A single allocation lets every failure clear all partial input, including
	// bytes an underlying reader wrote before returning its error.
	buffer := make([]byte, maxUtilityPasswordSize+1)
	n, err := io.ReadFull(input, buffer)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		clear(buffer)
		return nil, errors.New("could not read your password from standard input")
	}
	if n > maxUtilityPasswordSize {
		clear(buffer)
		return nil, errors.New("your password input exceeds the 64-kilobyte limit")
	}
	end := n
	if end > 0 && buffer[end-1] == '\n' {
		end--
		if end > 0 && buffer[end-1] == '\r' {
			end--
		}
	}
	secret := buffer[:end]
	if len(secret) == 0 || bytes.ContainsAny(secret, "\r\n\x00") {
		clear(buffer)
		return nil, errors.New("provide one nonempty password without line breaks or a zero byte on standard input")
	}
	clear(buffer[end:])
	return secret, nil
}
