package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
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
	styles     palette
	width      int
}

func newSetupModel(initial string) setupModel {
	input := textinput.New()
	input.Prompt = "Your mount destination: "
	input.Placeholder = "Enter an absolute directory"
	input.SetValue(initial)
	input.Focus()
	m := setupModel{input: input, width: 80}
	m.setPalette(outputPalette(os.Stdout))
	return m
}

func (m *setupModel) setPalette(styles palette) {
	m.styles = styles
	m.input.PromptStyle = styles.label
	m.input.TextStyle = styles.path
	m.input.PlaceholderStyle = styles.muted
	m.input.Cursor.Style = styles.label
}

func (m setupModel) Init() tea.Cmd { return textinput.Blink }

func (m setupModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if size, ok := message.(tea.WindowSizeMsg); ok {
		m.width = size.Width
		m.input.Width = max(8, size.Width-30)
		return m, nil
	}
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
	p := m.styles
	title := p.title.Render("Configure your pvedisk mount destination")
	if m.confirming {
		creation := ""
		if m.missing {
			creation = p.warning.Render(fmt.Sprintf("Create your missing directory %s?", m.canonical)) + "\n"
		}
		content := title + "\n" + p.muted.Render("2 / 2  Review and confirm") + "\n\n" + p.path.Render(layout(m.canonical)) + "\n" + creation + p.label.Render("Save this configuration"+confirmationSuffix(m.missing)) + " " + p.success.Render("[y/N]") + " " + p.muted.Render("(Escape cancels)")
		return p.panel(content, m.width) + "\n"
	}
	message := title + "\n" + p.muted.Render("1 / 2  Choose your destination") + "\n\n" + m.input.View() + "\n\n" + p.muted.Render("Enter continues; Escape cancels.") + "\n"
	if m.err != nil {
		message += "\n" + p.warning.Render(m.err.Error()) + "\n"
	}
	return p.panel(strings.TrimSuffix(message, "\n"), m.width) + "\n"
}

func confirmationSuffix(missing bool) string {
	if missing {
		return " and create your directory?"
	}
	return "?"
}

func wizard(input io.Reader, output io.Writer, initial string) (string, bool, error) {
	setup := newSetupModel(initial)
	setup.setPalette(outputPalette(output))
	model, err := tea.NewProgram(setup, tea.WithInput(input), tea.WithOutput(output)).Run()
	if err != nil {
		return "", false, fmt.Errorf("run your setup wizard: %w", err)
	}
	result := model.(setupModel)
	return result.canonical, result.confirmed, nil
}
