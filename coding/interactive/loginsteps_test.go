package interactive

import (
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
	return NewLoginDialogComponent(nil, "amazon-bedrock", func(bool, string) {}, "", "")
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

// ShowAuthPrompt routes a select prompt through the dialog and returns the id the
// login flow expects (upstream's ui.select).
func TestShowAuthPromptSelect(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()
	dialog := newLoginDialogForTest(t)
	// newAuthWiring wires the select step itself, which is the point.
	wiring := newAuthWiring(app)

	done := make(chan string, 1)
	errc := make(chan error, 1)
	go func() {
		value, err := wiring.ShowAuthPrompt(dialog, bedrockMethodPrompt)
		done <- value
		errc <- err
	}()

	// The prompt appears on the flow's goroutine, so wait for it before sending
	// keys (otherwise they land on the dialog's input instead of the list).
	deadline := time.Now().Add(2 * time.Second)
	for dialog.selectList == nil {
		if time.Now().After(deadline) {
			t.Fatal("the select prompt never appeared")
		}
		time.Sleep(time.Millisecond)
	}
	dialog.HandleInput("\r") // the first option
	select {
	case value := <-done:
		if err := <-errc; err != nil {
			t.Fatalf("err = %v", err)
		}
		if value != "bearer-token" {
			t.Errorf("value = %q, want bearer-token", value)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ShowAuthPrompt never returned")
	}
}

// A select prompt with no dialog seam reports a cancellation rather than hanging.
func TestShowAuthPromptSelectWithoutSeam(t *testing.T) {
	wiring := &AuthWiring{}
	if _, err := wiring.ShowAuthPrompt(nil, bedrockMethodPrompt); err == nil {
		t.Error("a missing select seam should cancel")
	}
}
