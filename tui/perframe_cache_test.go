package tui

import (
	"strings"
	"testing"
)

// TestSpacerRenderReusesItsLines pins the largest per-frame allocator: a
// transcript holds one spacer per message, and each rebuilt its blank block on
// every paint (a pure function of Lines).
func TestSpacerRenderReusesItsLines(t *testing.T) {
	spacer := NewSpacer(3)
	first := spacer.Render(40)
	if len(first) != 3 {
		t.Fatalf("spacer rendered %d lines, want 3", len(first))
	}
	if allocs := testing.AllocsPerRun(20, func() { _ = spacer.Render(40) }); allocs != 0 {
		t.Fatalf("warm spacer render allocated %.0f times; want 0", allocs)
	}
	if second := spacer.Render(40); &first[0] != &second[0] {
		t.Fatal("spacer rebuilt its lines instead of reusing them")
	}

	spacer.SetLines(1)
	if got := spacer.Render(40); len(got) != 1 {
		t.Fatalf("after SetLines the spacer rendered %d lines, want 1", len(got))
	}
	spacer.Invalidate()
	if got := spacer.Render(40); len(got) != 1 {
		t.Fatalf("after Invalidate the spacer rendered %d lines, want 1", len(got))
	}
}

// TestBoxCachesTheBackgroundSample pins that a warm box does not re-apply its
// background function just to fingerprint the cache.
func TestBoxCachesTheBackgroundSample(t *testing.T) {
	calls := 0
	bgFn := func(text string) string {
		calls++
		return "A" + text
	}
	box := NewBox(0, 0, bgFn)
	box.AddChild(NewText("content", 0, 0, nil))
	box.Render(20)

	warm := calls
	box.Render(20)
	box.Render(20)
	if calls != warm {
		t.Fatalf("the background sample was recomputed %d times on warm renders", calls-warm)
	}

	box.SetBgFn(func(text string) string {
		calls++
		return "B" + text
	})
	if rendered := strings.Join(box.Render(20), "\n"); !strings.Contains(rendered, "B") {
		t.Fatalf("background change not picked up: %q", rendered)
	}
	if calls == warm {
		t.Fatal("SetBgFn must drop the cached sample")
	}
}

// TestStripZonePrefix pins the literal fast path against the regexp it guards.
func TestStripZonePrefix(t *testing.T) {
	cases := []struct {
		line string
		want string
	}{
		{"hello", "hello"},
		{"", ""},
		{"\x1b]133;A\x07hello", "hello"},
		{"\x1b]133;A\x07\x1b]133;B\x07\x1b]133;C\x07hi", "hi"},
		{"\x1b]133;A\x1b\\hi", "hi"},
		// Only the start is stripped, and only [ABC] markers.
		{"mid\x1b]133;A\x07tail", "mid\x1b]133;A\x07tail"},
		{"\x1b]133;D\x07x", "\x1b]133;D\x07x"},
		{"\x1b]133;A", "\x1b]133;A"},
	}
	for _, tc := range cases {
		got := stripZonePrefix(tc.line)
		if got != tc.want {
			t.Fatalf("stripZonePrefix(%q) = %q, want %q", tc.line, got, tc.want)
		}
		if want := osc133ZonePrefix.ReplaceAllString(tc.line, ""); got != want {
			t.Fatalf("stripZonePrefix(%q) = %q, regexp gives %q", tc.line, got, want)
		}
	}
}
