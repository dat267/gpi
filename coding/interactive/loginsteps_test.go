package interactive

import (
	"errors"
	"testing"
	"time"

	"github.com/dat267/pier/ai"
)

func newLoginDialogForTest(t *testing.T) *LoginDialogComponent {
	t.Helper()
	SetCustomThemesDir(t.TempDir())
	SetRegisteredThemes(nil)
	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	InitTheme("dark", false)
	return NewLoginDialogComponent(nil, nil, "amazon-bedrock", func(bool, string) {}, "", "")
}

var bedrockMethodPrompt = ai.AuthPrompt{
	Type:    ai.AuthPromptSelect,
	Message: "Select Amazon Bedrock authentication method:",
	SelectOptions: []ai.AuthSelectOption{
		{ID: "bearer-token", Label: "Bearer token"},
		{ID: "aws-profile", Label: "AWS profile"},
		{ID: "credential-chain", Label: "Existing AWS credential chain"},
	},
}

// The select prompt lists the options and answers with the chosen id.
func TestLoginDialogSelect(t *testing.T) {
	dialog := newLoginDialogForTest(t)
	results := dialog.ShowSelect(bedrockMethodPrompt.Message, bedrockMethodPrompt.SelectOptions)

	dialog.HandleInput("\x1b[B") // down to the second option
	dialog.HandleInput("\r")

	select {
	case result := <-results:
		if result.Err != nil {
			t.Fatalf("err = %v", result.Err)
		}
		if result.Value != "aws-profile" {
			t.Errorf("value = %q, want aws-profile", result.Value)
		}
	case <-time.After(time.Second):
		t.Fatal("the select never answered")
	}
	// A further key must not reach the list once it is done.
	dialog.HandleInput("\r")
}

// Escape cancels the whole login, which is what the select step reports as an
// abort.
func TestLoginDialogSelectCancel(t *testing.T) {
	dialog := newLoginDialogForTest(t)
	dialog.ShowSelect(bedrockMethodPrompt.Message, bedrockMethodPrompt.SelectOptions)

	dialog.HandleInput("\x1b")

	select {
	case <-dialog.Aborted():
	case <-time.After(time.Second):
		t.Fatal("escape did not cancel the login")
	}
}

// The auth wiring wires the select step, and ShowAuthPrompt routes a select
// prompt through it and returns the id the login flow switches on.
//
// The dialog is driven on the flow's own goroutine here. In the app the flow and
// the input handling are both on the UI loop, so driving it from a second
// goroutine would be a race this code does not have — and CI's -race detector
// duly flagged the first version of this test.
func TestShowAuthPromptSelect(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	wiring := newAuthWiring(app)
	wiring.ShowAuthSelect = func(dialog *LoginDialogComponent, prompt ai.AuthPrompt) (string, error) {
		results := dialog.ShowSelect(prompt.Message, prompt.SelectOptions)
		dialog.HandleInput("\r") // the first option, as the UI loop would deliver it
		select {
		case result := <-results:
			if result.Err != nil {
				return "", errors.New("Login cancelled")
			}
			return result.Value, nil
		case <-dialog.Aborted():
			return "", errors.New("Login cancelled")
		}
	}

	value, err := wiring.ShowAuthPrompt(newLoginDialogForTest(t), bedrockMethodPrompt)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if value != "bearer-token" {
		t.Errorf("value = %q, want bearer-token", value)
	}
}

// The app's own select step is wired (the adapter itself only forwards to the
// dialog, which the test above drives).
func TestAuthSelectStepIsWired(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()
	if newAuthWiring(app).ShowAuthSelect == nil {
		t.Fatal("the auth wiring has no select step, so a provider that asks one cannot log in")
	}
}

// A select prompt with no dialog seam reports a cancellation rather than hanging.
func TestShowAuthPromptSelectWithoutSeam(t *testing.T) {
	wiring := &AuthWiring{}
	if _, err := wiring.ShowAuthPrompt(nil, bedrockMethodPrompt); err == nil {
		t.Error("a missing select seam should cancel")
	}
}
