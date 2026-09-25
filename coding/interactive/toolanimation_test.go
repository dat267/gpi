package interactive

import (
	"testing"
	"time"
)

// TestToolExecutionComponentAnimatesWhileRunning pins the tool-level animation:
// a running tool reports a 1s frame so the shell elapsed label repaints, and a
// finished or not-yet-started tool does not.
func TestToolExecutionComponentAnimatesWhileRunning(t *testing.T) {
	component := &ToolExecutionComponent{}
	if needs, _ := component.AnimationFrame(time.Now()); needs {
		t.Fatal("a tool that has not started animates")
	}
	component.executionStarted = true
	component.isPartial = true
	needs, delay := component.AnimationFrame(time.Now())
	if !needs || delay != time.Second {
		t.Fatalf("running tool: needs=%v delay=%v", needs, delay)
	}
	component.isPartial = false
	if needs, _ := component.AnimationFrame(time.Now()); needs {
		t.Fatal("a finished tool animates")
	}
}
