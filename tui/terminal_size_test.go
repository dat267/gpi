package tui

import "testing"

// The terminal size is cached so the render path never calls the console: on
// Windows a size query (GetConsoleScreenBufferInfo) can block for as long as a
// mouse selection is active, and the render path must not block. The cache is
// primed at Start and refreshed by the resize watcher; only a real change
// repaints.

func TestProcessTerminalSizeIsCached(t *testing.T) {
	terminal := NewProcessTerminal(nil, nil)
	queries := 0
	width, height := 100, 40
	terminal.sizeFn = func() (int, int) {
		queries++
		return width, height
	}

	if got := terminal.Columns(); got != 100 {
		t.Fatalf("Columns() = %d, want 100", got)
	}
	if got := terminal.Rows(); got != 40 {
		t.Fatalf("Rows() = %d, want 40", got)
	}
	if queries != 1 {
		t.Fatalf("size queried %d times, want 1 (Rows must reuse the cache)", queries)
	}

	// The cache is stable while the size is unchanged.
	_ = terminal.Columns()
	_ = terminal.Rows()
	if queries != 1 {
		t.Fatalf("size queried %d times after a cached read, want 1", queries)
	}

	// A refresh picks up the change; the watcher uses the bool to repaint only
	// when it actually moved.
	width = 120
	if !terminal.refreshSize() {
		t.Fatal("refreshSize did not report a changed size")
	}
	if got := terminal.Columns(); got != 120 {
		t.Fatalf("Columns() = %d after refresh, want 120", got)
	}
	if queries != 2 {
		t.Fatalf("size queried %d times, want 2 after one refresh", queries)
	}
	if terminal.refreshSize() {
		t.Fatal("refreshSize reported a change with an unchanged size")
	}
}

func TestProcessTerminalSizeFallsBackWhenQueryFails(t *testing.T) {
	t.Setenv("COLUMNS", "")
	t.Setenv("LINES", "")
	terminal := NewProcessTerminal(nil, nil)
	terminal.sizeFn = func() (int, int) { return 0, 0 }
	terminal.refreshSize()
	// querySize defaults to 80x24 when the query yields nothing.
	if got := terminal.Columns(); got != 80 {
		t.Fatalf("Columns() = %d, want 80", got)
	}
	if got := terminal.Rows(); got != 24 {
		t.Fatalf("Rows() = %d, want 24", got)
	}
}
