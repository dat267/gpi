package coding

import "testing"

func TestFormatCLIDiagnostic(t *testing.T) {
	cases := []struct {
		name string
		in   CLIDiagnostic
		want string
	}{
		{"error", CLIDiagnostic{Type: "error", Message: "Unknown option: --nope"}, "\x1b[31mError: Unknown option: --nope\x1b[0m"},
		{"warning", CLIDiagnostic{Type: "warning", Message: "Invalid thinking level"}, "\x1b[33mWarning: Invalid thinking level\x1b[0m"},
		// Upstream falls through to dim with no prefix for any other type.
		{"other", CLIDiagnostic{Type: "info", Message: "note"}, "\x1b[2mnote\x1b[0m"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FormatCLIDiagnostic(tc.in); got != tc.want {
				t.Errorf("FormatCLIDiagnostic = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHasErrorDiagnostics(t *testing.T) {
	cases := []struct {
		name        string
		diagnostics []CLIDiagnostic
		want        bool
	}{
		{"none", nil, false},
		{"warning only", []CLIDiagnostic{{Type: "warning", Message: "w"}}, false},
		{"an error", []CLIDiagnostic{{Type: "warning", Message: "w"}, {Type: "error", Message: "e"}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasErrorDiagnostics(tc.diagnostics); got != tc.want {
				t.Errorf("HasErrorDiagnostics = %v, want %v", got, tc.want)
			}
		})
	}
}

// The parse diagnostics that reach the CLI are the ones a user can actually
// trip, so the formatting above is checked against a real parse rather than a
// hand-built value. A single-dash option is what reports an unknown option —
// an unknown --flag is held back for extensions instead.
func TestParseArgsDiagnosticsAreFormatted(t *testing.T) {
	args := ParseArgs([]string{"-z"})
	if len(args.Diagnostics) != 1 {
		t.Fatalf("diagnostics = %#v, want one", args.Diagnostics)
	}
	want := "\x1b[31mError: Unknown option: -z\x1b[0m"
	if got := FormatCLIDiagnostic(args.Diagnostics[0]); got != want {
		t.Errorf("formatted = %q, want %q", got, want)
	}
	if !HasErrorDiagnostics(args.Diagnostics) {
		t.Error("an unknown option should be fatal")
	}
}
