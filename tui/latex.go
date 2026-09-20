package tui

// Port of src/latex.ts: the LaTeX math-to-Unicode renderer.
//
// The full port (symbol tables, the LaTeX parser, and the fraction/root/
// script/matrix layout engine) is scheduled separately; until then RenderLatex
// reports "unsupported" for every expression, which is exactly the upstream
// fallback path: the Markdown renderer emits the raw LaTeX source
// (divergence D68).

func renderLatexImpl(source string, options RenderLatexOptions) (string, bool) {
	return "", false
}
