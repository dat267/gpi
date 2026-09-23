package coding

// Port of the CLI's diagnostic reporting: upstream main.ts prints the parse
// diagnostics straight after parseArgs and treats any error among them as
// fatal, and reportDiagnostics() prints the runtime ones with the same shape.

const (
	ansiRed    = "\x1b[31m"
	ansiYellow = "\x1b[33m"
	ansiDim    = "\x1b[2m"
	ansiReset  = "\x1b[0m"
)

// FormatCLIDiagnostic renders one diagnostic the way upstream's CLI does:
// errors and warnings carry a matching colour and an "Error: "/"Warning: "
// prefix, and any other type is dimmed with no prefix.
func FormatCLIDiagnostic(d CLIDiagnostic) string {
	color, prefix := ansiDim, ""
	switch d.Type {
	case "error":
		color, prefix = ansiRed, "Error: "
	case "warning":
		color, prefix = ansiYellow, "Warning: "
	}
	return color + prefix + d.Message + ansiReset
}

// HasErrorDiagnostics reports whether any diagnostic is an error, which is what
// upstream treats as a fatal argument error.
func HasErrorDiagnostics(diagnostics []CLIDiagnostic) bool {
	for _, d := range diagnostics {
		if d.Type == "error" {
			return true
		}
	}
	return false
}
