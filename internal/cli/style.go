package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"golang.org/x/term"
)

type palette struct {
	enabled                                     bool
	title, label, path, success, warning, muted lipgloss.Style
	key, value, number, boolean, null           lipgloss.Style
}

func colorEnabled(output io.Writer) bool {
	if _, present := os.LookupEnv("NO_COLOR"); present {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	file, ok := output.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

func newPalette(output io.Writer, enabled bool) palette {
	renderer := lipgloss.NewRenderer(output)
	if enabled {
		renderer.SetColorProfile(termenv.ANSI256)
	} else {
		renderer.SetColorProfile(termenv.Ascii)
	}
	style := func(color string) lipgloss.Style { return renderer.NewStyle().Foreground(lipgloss.Color(color)) }
	return palette{
		enabled: enabled,
		title:   style("81").Bold(true), label: style("117").Bold(true), path: style("159"),
		success: style("114").Bold(true), warning: style("221"), muted: style("245"),
		key: style("81"), value: style("114"), number: style("215"), boolean: style("177"), null: style("245"),
	}
}

func outputPalette(output io.Writer) palette { return newPalette(output, colorEnabled(output)) }

var jsonToken = regexp.MustCompile("\"(?:\\\\.|[^\"\\\\])*\"|-?[0-9]+(?:\\.[0-9]+)?(?:[eE][+-]?[0-9]+)?|true|false|null")

func (p palette) json(data []byte) string {
	text := string(data)
	if !p.enabled {
		return text
	}
	var output strings.Builder
	previous := 0
	for _, span := range jsonToken.FindAllStringIndex(text, -1) {
		output.WriteString(text[previous:span[0]])
		token := text[span[0]:span[1]]
		style := p.number
		switch {
		case strings.HasPrefix(token, "\""):
			style = p.value
			if strings.HasPrefix(strings.TrimLeft(text[span[1]:], " \t\r\n"), ":") {
				style = p.key
			}
		case token == "true" || token == "false":
			style = p.boolean
		case token == "null":
			style = p.null
		}
		output.WriteString(style.Render(token))
		previous = span[1]
	}
	output.WriteString(text[previous:])
	return output.String()
}

func writeStatus(output io.Writer, state any) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, outputPalette(output).json(data))
	return err
}

func styledHelp(output io.Writer, text string) string {
	p := outputPalette(output)
	if !p.enabled {
		return text
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if i == 0 || (line != "" && !strings.HasPrefix(line, " ") && strings.HasSuffix(line, ":")) {
			lines[i] = p.title.Render(line)
		}
	}
	return strings.Join(lines, "\n")
}

// PrintError renders terminal errors clearly while keeping machine logs structured.
// It reports whether a terminal message was written so callers can retain slog
// for redirected output and detached worker logs.
func PrintError(output io.Writer, err error) bool {
	file, ok := output.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return false
	}
	p := outputPalette(output)
	fmt.Fprintf(output, "%s %s\n", p.warning.Bold(true).Render("Error:"), err)
	return true
}
