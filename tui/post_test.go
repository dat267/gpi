package tui

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestPostRunsSerializedWithRenders hammers the renderer from a goroutine
// while posting mutations; the posted callbacks must never run concurrently
// with a paint or another posted callback.
func TestPostRunsSerializedWithRenders(t *testing.T) {
	terminal := &fakePostTerminal{width: 40, height: 10}
	screen := NewMainScreen(terminal, false, "")
	var inPaint, inCallback int32
	var overlap int32
	inside := func() bool {
		if atomic.AddInt32(&inPaint, 1) != 1 {
			atomic.StoreInt32(&overlap, 1)
		}
		time.Sleep(time.Millisecond)
		atomic.AddInt32(&inPaint, -1)
		return atomic.LoadInt32(&overlap) == 1
	}
	// The paint marker runs inside every render.
	screen.DoRender = func() {
		if inside() {
			t.Error("overlapping render/callback detected")
		}
	}
	screen.Start()
	defer screen.Stop(TuiStopOptions{})

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				screen.Post(func() {
					if atomic.AddInt32(&inCallback, 1) != 1 || atomic.LoadInt32(&inPaint) != 0 {
						atomic.StoreInt32(&overlap, 1)
					}
					time.Sleep(time.Millisecond)
					atomic.AddInt32(&inCallback, -1)
				})
				time.Sleep(time.Millisecond)
			}
		}()
	}
	wg.Wait()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		screen.RenderNow(true)
		pending := len(screen.posted)
		if pending == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	pending := len(screen.posted)
	if pending != 0 {
		t.Fatalf("%d posted callbacks never ran", pending)
	}
	if atomic.LoadInt32(&overlap) == 1 {
		t.Fatal("callbacks ran concurrently with renders or each other")
	}
}

// TestPostCallbackMayRequestRender asserts a posted callback can call
// RequestRender without deadlocking.
func TestPostCallbackMayRequestRender(t *testing.T) {
	terminal := &fakePostTerminal{width: 40, height: 10}
	screen := NewMainScreen(terminal, false, "")
	screen.EnableRenderTicks()
	done := make(chan struct{})
	drain := make(chan struct{})
	go func() {
		defer close(drain)
		for screen.RenderCount() == 0 {
			<-screen.RenderTicks()
			screen.RenderNow(false)
		}
	}()
	screen.Start()
	defer func() {
		<-drain // the owner loop stops painting before Stop reads render state
		screen.Stop(TuiStopOptions{})
	}()
	screen.Post(func() {
		screen.RequestRender(false)
		close(done)
	})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("posted callback deadlocked")
	}
}

type fakePostTerminal struct {
	width, height int
}

func (f *fakePostTerminal) Start(onInput func(string), onResize func()) {}
func (f *fakePostTerminal) Stop()                                       {}
func (f *fakePostTerminal) DrainInput(maxMs int, idleMs int) error      { return nil }
func (f *fakePostTerminal) Write(data string)                           {}
func (f *fakePostTerminal) Columns() int                                { return f.width }
func (f *fakePostTerminal) Rows() int                                   { return f.height }
func (f *fakePostTerminal) KittyProtocolActive() bool                   { return false }
func (f *fakePostTerminal) MoveBy(lines int)                            {}
func (f *fakePostTerminal) HideCursor()                                 {}
func (f *fakePostTerminal) ShowCursor()                                 {}
func (f *fakePostTerminal) ClearLine()                                  {}
func (f *fakePostTerminal) ClearFromCursor()                            {}
func (f *fakePostTerminal) ClearScreen()                                {}
func (f *fakePostTerminal) SetTitle(title string)                       {}
func (f *fakePostTerminal) SetProgress(active bool)                     {}
