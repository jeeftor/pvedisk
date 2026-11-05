package main

import (
	"log/slog"
	"os"

	"github.com/jeeftor/pvedisk/internal/cli"
)

func main() {
	if err := cli.NewCommand().Execute(); err != nil {
		if !cli.PrintError(os.Stderr, err) {
			slog.Error("pvedisk failed", "error", err)
		}
		os.Exit(cli.ExitCode(err))
	}
}
