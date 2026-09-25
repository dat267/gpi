package interactive

import (
	"testing"

	"github.com/dat267/pier/tui"
)

// BenchmarkTranscriptFrameScaling measures the warm fullscreen frame cost at
// growing transcript sizes. A frame re-walks every mounted component to detect
// changes (each component then returns its cached lines, and Markdown reports
// 100% cache hits), so the cost is O(components), not O(visible lines):
// ~0.17 ms at 1k components, ~4.5 ms at 16k. A single paint is already windowed
// to the terminal height; what scales is the change-detection walk. This pins
// the curve so a regression, or the viewport/versioned-cache fix described in
// AGENTS.md, is visible.
func BenchmarkTranscriptFrameScaling(b *testing.B) {
	for _, messages := range []int{500, 2000, 8000} {
		b.Run(itoa(messages)+"msgs", func(b *testing.B) {
			app, cleanup := newTestAppB(b)
			defer cleanup()
			if screen, ok := app.initialUI.(*tui.AltScreen); ok {
				screen.Start()
				screen.DisableAutoRender()
			}
			buildScrollTranscriptN(b, app, messages)
			app.UI.RenderNow(false) // warm every cache
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				app.UI.RenderNow(false)
			}
		})
	}
}
