package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"testing"

	"github.com/jeeftor/pvedisk/internal/session"
)

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func TestJSONColorsPreserveEveryByteAndValue(t *testing.T) {
	plain := []byte("{\n  \"key\": \"escaped \\\"quote\\\" and \\\\ path; true null 42\",\n  \"number\": -1.25e+3,\n  \"boolean\": true,\n  \"empty\": null,\n  \"items\": [false, 42, \"value\"]\n}")
	if !json.Valid(plain) {
		t.Fatal("invalid fixture")
	}
	p := newPalette(io.Discard, true)
	colored := p.json(plain)
	if !strings.Contains(colored, "\x1b[") {
		t.Fatal("missing ANSI colors")
	}
	stripped := ansiEscape.ReplaceAllString(colored, "")
	if stripped != string(plain) || !json.Valid([]byte(stripped)) {
		t.Fatalf("JSON changed: %s", stripped)
	}
	for _, token := range []string{
		p.key.Render(`"key"`),
		p.value.Render(`"escaped \"quote\" and \\ path; true null 42"`),
		p.number.Render("-1.25e+3"),
		p.boolean.Render("true"),
		p.null.Render("null"),
	} {
		if !strings.Contains(colored, token) {
			t.Errorf("missing correctly colored token %q in %q", token, colored)
		}
	}
	if p.key.Render(`"key"`) == p.value.Render(`"key"`) {
		t.Fatal("keys and string values must have distinct colors")
	}
}

func TestJSONWithoutColorsIsUnchanged(t *testing.T) {
	plain := []byte(`{"string":"value","number":10,"boolean":false,"null":null}`)
	if got := newPalette(io.Discard, false).json(plain); got != string(plain) {
		t.Fatalf("plain JSON changed: %q", got)
	}
}

func TestRedirectedStatusIsIndentedJSON(t *testing.T) {
	ops := testOperations()
	state := &session.State{VMID: 400, Phase: "mounted", Public: "/repairs/vm400", ReadOnly: true}
	ops.status = func(int) (*session.State, error) { return state, nil }
	output, err := execute(ops, "status", "400")
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid([]byte(output)) || strings.Contains(output, "\x1b") || !strings.Contains(output, "\n  \"vmid\": 400") {
		t.Fatalf("invalid redirected output: %q", output)
	}
	var decoded session.State
	if err := json.Unmarshal([]byte(output), &decoded); err != nil || decoded.VMID != 400 || decoded.Phase != "mounted" || !decoded.ReadOnly {
		t.Fatalf("state changed: %+v err=%v", decoded, err)
	}
}

func TestColorOptOut(t *testing.T) {
	for _, variable := range []string{"NO_COLOR", "TERM"} {
		t.Run(variable, func(t *testing.T) {
			if variable == "NO_COLOR" {
				t.Setenv(variable, "1")
			} else {
				t.Setenv(variable, "dumb")
			}
			if colorEnabled(io.Discard) {
				t.Fatal("color enabled with opt-out")
			}
			var output bytes.Buffer
			if err := writeStatus(&output, map[string]any{"number": 42}); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(output.String(), "\x1b") || !json.Valid(output.Bytes()) {
				t.Fatalf("not plain JSON: %q", output.String())
			}
		})
	}
}

func TestWizardUsesPaletteWithoutChangingConfirmation(t *testing.T) {
	model := newSetupModel("/repairs")
	model.setPalette(newPalette(io.Discard, true))
	if view := model.View(); !strings.Contains(view, "\x1b[") || !strings.Contains(ansiEscape.ReplaceAllString(view, ""), "1 / 2") {
		t.Fatalf("missing styled destination step: %q", view)
	}
	model.confirming = true
	model.canonical = "/repairs"
	model.missing = true
	view := ansiEscape.ReplaceAllString(model.View(), "")
	for _, want := range []string{"2 / 2", "Create your missing directory /repairs?", "[y/N]", "vm400-extra"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q in %q", want, view)
		}
	}
}

func TestRedirectedErrorsRemainAvailableForStructuredLogging(t *testing.T) {
	var output bytes.Buffer
	if PrintError(&output, errors.New("failure")) || output.Len() != 0 {
		t.Fatalf("consumed redirected error: %q", output.String())
	}
}
