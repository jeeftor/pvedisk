package main

import (
	"log/slog"
	"os"

	"github.com/jeeftor/pvedisk/internal/cli"
)

func main() {
	if err := cli.NewCommand().Execute(); err != nil {
		slog.Error("pvedisk failed", "error", err)
		os.Exit(1)
	}
}
