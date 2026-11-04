package cli

import (
	"fmt"
	"io"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jeeftor/pvedisk/internal/config"
)

type setupModel struct {
	input      textinput.Model
	canonical  string
	missing    bool
	confirming bool
	confirmed  bool
	cancelled  bool
	err        error
}

func newSetupModel(initial string) setupModel {
	input := textinput.New()
	input.Prompt = "Your mount destination: "
	input.Placeholder = "Enter an absolute directory"
	input.SetValue(initial)
	input.Focus()
	return setupModel{input: input}
}

func (m setupModel) Init() tea.Cmd { return textinput.Blink }

func (m setupModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := message.(tea.KeyMsg); ok {
		switch key.String() {
		case "ctrl+c", "esc":
			m.cancelled = true
			return m, tea.Quit
		case "enter":
			if !m.confirming {
				canonical, exists, err := config.ValidateRoot(m.input.Value())
				m.err = err
				if err != nil {
					return m, nil
				}
				m.canonical, m.missing, m.confirming = canonical, !exists, true
				return m, nil
			}
			m.cancelled = true
			return m, tea.Quit
		case "y", "Y":
			if m.confirming {
				m.confirmed = true
				return m, tea.Quit
			}
		case "n", "N":
			if m.confirming {
				m.cancelled = true
				return m, tea.Quit
			}
		}
	}
	if !m.confirming {
		var command tea.Cmd
		m.input, command = m.input.Update(message)
		return m, command
	}
	return m, nil
}

func (m setupModel) View() string {
	if m.cancelled || m.confirmed {
		return ""
	}
	title := lipgloss.NewStyle().Bold(true).Render("Configure your pvedisk mount destination")
	if m.confirming {
		creation := ""
		if m.missing {
			creation = fmt.Sprintf("Create your missing directory %s?\n", m.canonical)
		}
		return title + "\n\n" + layout(m.canonical) + "\n" + creation + "Save this configuration" + confirmationSuffix(m.missing) + " [y/N] (Escape cancels)\n"
	}
	message := title + "\n\n" + m.input.View() + "\n\nEnter continues; Escape cancels.\n"
	if m.err != nil {
		message += fmt.Sprintf("\n%s\n", m.err)
	}
	return message
}

func confirmationSuffix(missing bool) string {
	if missing {
		return " and create your directory?"
	}
	return "?"
}

func wizard(input io.Reader, output io.Writer, initial string) (string, bool, error) {
	model, err := tea.NewProgram(newSetupModel(initial), tea.WithInput(input), tea.WithOutput(output)).Run()
	if err != nil {
		return "", false, fmt.Errorf("run your setup wizard: %w", err)
	}
	result := model.(setupModel)
	return result.canonical, result.confirmed, nil
}
