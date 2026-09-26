//go:build linux

package interactive

// PTY watchdog tests for the D136-D139 deadlock class. Each test drives the
// real pier binary through a flow inside a pseudo-terminal, sends SIGQUIT at
// teardown, and fails when the runtime stack dump shows a goroutine blocked
// on a sync.Mutex. The harness is Linux-specific: it unlocks the pty master
// with TIOCSPTLCK and reads the slave name with TIOCGPTN, neither of which the
// BSDs have.

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// ptySession is one pier process attached to a pseudo-terminal.
type ptySession struct {
	t      *testing.T
	cmd    *exec.Cmd
	master int

	mu   sync.Mutex
	buf  bytes.Buffer
	done chan struct{}
}

var (
	ptyBinaryOnce sync.Once
	ptyBinaryPath string
	ptyBinaryErr  error
)

// pierBinary resolves the pier binary for the watchdog tests: $PIER_TEST_BIN,
// then ./bin/pier relative to the module root, then a fresh build into a
// temporary directory. Tests skip when no binary can be produced so the gate
// never depends on building pier twice.
func pierBinary(t *testing.T) string {
	t.Helper()
	ptyBinaryOnce.Do(func() {
		executable := func(path string) bool {
			info, err := os.Stat(path)
			return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
		}
		// A non-executable candidate must fall through to a fresh build:
		// `go build -o` onto an existing file preserves its mode, so a 0644
		// bin/pier would otherwise surface as a fork/exec "permission denied"
		// skip in every test.
		if env := os.Getenv("PIER_TEST_BIN"); env != "" && executable(env) {
			ptyBinaryPath, ptyBinaryErr = env, nil
			return
		}
		root := findModuleRoot(t)
		if root != "" {
			candidate := filepath.Join(root, "bin", "pier")
			if executable(candidate) {
				ptyBinaryPath, ptyBinaryErr = candidate, nil
				return
			}
		}
		// Build once into the shared temp dir (reused across -count runs).
		dir := filepath.Join(os.TempDir(), "pier-pty-watchdog")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			ptyBinaryErr = err
			return
		}
		bin := filepath.Join(dir, "pier")
		if root == "" {
			ptyBinaryErr = fmt.Errorf("module root not found")
			return
		}
		build := exec.Command("go", "build", "-o", bin, ".")
		build.Dir = root
		if out, err := build.CombinedOutput(); err != nil {
			ptyBinaryErr = fmt.Errorf("build pier: %v: %s", err, out)
			return
		}
		if err := os.Chmod(bin, 0o755); err != nil {
			ptyBinaryErr = err
			return
		}
		ptyBinaryPath, ptyBinaryErr = bin, nil
	})
	if ptyBinaryErr != nil {
		t.Skipf("pier binary unavailable: %v", ptyBinaryErr)
	}
	return ptyBinaryPath
}

func findModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// startPier launches pier attached to a new pty with an isolated agent dir.
func startPier(t *testing.T, args ...string) *ptySession {
	t.Helper()
	return startPierConfigured(t, nil, args...)
}

// startPierConfigured launches pier with an isolated agent dir that setup may
// pre-populate (e.g. settings.json) before the child starts.
func startPierConfigured(t *testing.T, setup func(agentDir string), args ...string) *ptySession {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("pty watchdog tests require a unix pty")
	}

	bin := pierBinary(t)
	agentDir := t.TempDir()
	cwd := t.TempDir()
	if setup != nil {
		setup(agentDir)
	}

	master, slave, err := openPty()
	if err != nil {
		t.Skipf("pty unavailable: %v", err)
	}
	slaveFile := os.NewFile(uintptr(slave), "pty-slave")
	// The master is wrapped as an os.File so teardown can set a read deadline
	// and unblock the reader goroutine deterministically (a raw close does not
	// interrupt an in-flight read on Linux).
	masterFile := os.NewFile(uintptr(master), "pty-master")
	session := &ptySession{t: t, master: master, done: make(chan struct{})}

	// Teardown: kill the child, close the last slave fd, unblock the reader via
	// a past master read deadline, wait for it, then close the master. Order
	// matters: on the skip path (Start failed) there is no child and no EOF,
	// so waiting on done before unblocking the reader deadlocked the harness.
	t.Cleanup(func() {
		if session.cmd != nil {
			_ = session.cmd.Process.Kill()
		}
		_ = slaveFile.Close()
		_ = masterFile.SetReadDeadline(time.Now().Add(-1))
		select {
		case <-session.done:
		case <-time.After(2 * time.Second):
			// The reader stays blocked; bounded so a harness bug cannot hang
			// the test run.
		}
		_ = masterFile.Close()
	})

	cmd := exec.Command(bin, append([]string{"--offline"}, args...)...)
	cmd.Dir = cwd
	cmd.Stdin = slaveFile
	cmd.Stdout = slaveFile
	cmd.Stderr = slaveFile
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"PI_CODING_AGENT_DIR="+agentDir,
		"GOTRACEBACK=all",
	)
	// Own session + controlling terminal so raw mode, SIGWINCH and SIGQUIT
	// behave like a real run; the pty master sees everything the child writes.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		t.Skipf("start pier: %v", err)
	}
	session.cmd = cmd
	// The child holds its own dup; dropping the parent copy lets the reader
	// observe EOF as soon as the child exits.
	_ = slaveFile.Close()

	go func() {
		defer close(session.done)
		buf := make([]byte, 65536)
		for {
			n, err := masterFile.Read(buf)
			if n > 0 {
				session.mu.Lock()
				session.buf.Write(buf[:n])
				session.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()

	return session
}

// send writes keystrokes to the pty.
func (s *ptySession) send(data string) {
	s.t.Helper()
	if _, err := unix.Write(s.master, []byte(data)); err != nil {
		s.t.Fatalf("pty write: %v", err)
	}
}

// output returns everything the app has written so far.
func (s *ptySession) output() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// ansiPattern matches the sequences a terminal adds around text (OSC 8
// hyperlinks, SGR colour, cursor and mode changes) so a match can ignore them.
var ansiPattern = regexp.MustCompile(`\x1b\][^\x07]*\x07|\x1b\[[0-9;?]*[a-zA-Z]|\x1b[()][A-Z0-9]`)

// unwrapped removes what the terminal itself adds: escape sequences, and the
// line breaks and the indent/trailing padding that a wrapped token is surrounded
// by — the box indents every line, so the two halves of a wrapped path are
// separated by that indent as well as by the newline.
// The trust prompt prints a project path, which is longer than the terminal on a
// device whose temporary directory is deep, so the path arrives split
// mid-segment — a substring of it can never be found contiguously in the raw
// stream. Matching the unwrapped text is what makes these assertions about
// behaviour rather than about the terminal's width.
func unwrapped(output string) string {
	output = ansiPattern.ReplaceAllString(output, "")
	var joined strings.Builder
	for _, line := range strings.Split(output, "\n") {
		joined.WriteString(strings.TrimSpace(line))
	}
	return joined.String()
}

// waitForOutput polls until substr appears in the output or the timeout
// elapses; reports whether it was seen.
func (s *ptySession) waitForOutput(substr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(s.output(), substr) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return strings.Contains(s.output(), substr)
}

// waitForWrappedOutput is waitForOutput for a token the terminal wraps: a long
// path is split across lines and surrounded by the box's indent, so it can never
// be found contiguously in the raw stream. Only the tests that name such a token
// use this; matching every assertion unwrapped would loosen the others.
func (s *ptySession) waitForWrappedOutput(substr string, timeout time.Duration) bool {
	want := unwrapped(substr)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(unwrapped(s.output()), want) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return strings.Contains(unwrapped(s.output()), want)
}

// typeAndSubmit types text and presses Enter twice: the first Enter accepts
// the autocomplete completion when it is open, the second submits.
func (s *ptySession) typeAndSubmit(text string) {
	s.send(text)
	time.Sleep(250 * time.Millisecond)
	s.send("\r")
	time.Sleep(200 * time.Millisecond)
	s.send("\r")
}

// quitAndDump sends SIGQUIT and returns everything the process wrote after
// the signal (the goroutine dump lands on stderr, which is the pty).
// alreadyExited reports whether the process was gone before the signal.
func (s *ptySession) quitAndDump(timeout time.Duration) (dump string, alreadyExited bool) {
	if s.exited() {
		return "", true
	}
	_ = s.cmd.Process.Signal(unix.SIGQUIT)
	mark := len(s.output())
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if s.exited() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()[mark:], false
}

func (s *ptySession) exited() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// mutexBlockedRe matches a goroutine parked while acquiring a sync.Mutex or
// sync.RWMutex (runtime_SemacquireMutex is the parking primitive both use;
// plain Semacquire would also match WaitGroups, which are unrelated).
var mutexBlockedRe = regexp.MustCompile(`sync\.runtime_SemacquireMutex`)

// assertNoMutexBlocked fails when the SIGQUIT dump shows a goroutine blocked
// on a mutex.
func assertNoMutexBlocked(t *testing.T, dump string) {
	t.Helper()
	if !strings.Contains(dump, "SIGQUIT: quit") {
		t.Fatalf("expected a SIGQUIT goroutine dump, got: %.400s", dump)
	}
	if !mutexBlockedRe.MatchString(dump) {
		return
	}
	// Extract the offending goroutine blocks for the failure message.
	blocks := dump
	if groups := regexp.MustCompile(`(?s)goroutine \d+ gp=.*?(?=\ngoroutine \d+ gp=|\z)`).FindAllString(dump, -1); len(groups) > 0 {
		var blocked []string
		for _, g := range groups {
			if mutexBlockedRe.MatchString(g) {
				blocked = append(blocked, g)
			}
		}
		blocks = strings.Join(blocked, "\n\n")
	}
	t.Fatalf("goroutines blocked on sync.Mutex after SIGQUIT:\n%s", truncate(blocks, 4000))
}

// stripAnsiForLog removes escape sequences so failures show readable state.
func stripAnsiForLog(s string) string {
	return ansiSequenceRe.ReplaceAllString(s, "")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n... (truncated)"
}

// openPty allocates a pseudo-terminal pair through /dev/ptmx (the x/sys/unix
// surface has no posix_openpt): unlock with TIOCSPTLCK, derive the slave path
// from TIOCGPTN. devpts ownership follows the opening user, so grantpt is
// unnecessary on Linux and Darwin.
func openPty() (master int, slave int, err error) {
	master, err = unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		return -1, -1, err
	}
	defer func() {
		if err != nil {
			unix.Close(master)
		}
	}()
	if err = unix.IoctlSetPointerInt(master, unix.TIOCSPTLCK, 0); err != nil {
		return -1, -1, err
	}
	ptn, err := unix.IoctlGetInt(master, unix.TIOCGPTN)
	if err != nil {
		return -1, -1, err
	}
	name := fmt.Sprintf("/dev/pts/%d", ptn)
	slave, err = unix.Open(name, unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		return -1, -1, err
	}
	// Best-effort window size so the layout renders at a sane width.
	ws := &unix.Winsize{Row: 30, Col: 90}
	_ = unix.IoctlSetWinsize(master, unix.TIOCSWINSZ, ws)
	return master, slave, nil
}

// startFullscreenPier starts pier with fullscreen TUI mode so scroll and
// selector flows exercise the alt-screen renderer.
func startFullscreenPier(t *testing.T) *ptySession {
	t.Helper()
	return startPierConfigured(t, func(agentDir string) {
		settings := `{"tuiMode":"fullscreen"}`
		if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(settings), 0o644); err != nil {
			t.Fatalf("write settings: %v", err)
		}
	})
}

// TestPTYEditorSubmitNoMutexDeadlock drives the D136 flow: type a prompt and
// submit it, then dump stacks. A mutex-blocked goroutine fails the test.
func TestPTYEditorSubmitNoMutexDeadlock(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	session := startPier(t)
	if !session.waitForOutput("v0.0.0", 10*time.Second) {
		t.Fatalf("pier did not start:\n%.600s", session.output())
	}
	session.typeAndSubmit("hello from the watchdog test")
	time.Sleep(1500 * time.Millisecond)

	dump, exited := session.quitAndDump(5 * time.Second)
	if exited {
		t.Fatalf("pier exited before SIGQUIT after a submit; output:\n%.1500s", stripAnsiForLog(session.output()))
	}
	assertNoMutexBlocked(t, dump)
}

// TestPTYBackgroundProbeNoInputCorruption drives the D165 flow against the real
// binary: the auto theme's OSC 11 query is written after startup, the terminal
// reply is consumed (not typed into the editor), and input still works.
func TestPTYBackgroundProbeNoInputCorruption(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	session := startPierConfigured(t, func(agentDir string) {
		settings := `{"theme":"light/dark"}`
		if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(settings), 0o644); err != nil {
			t.Fatalf("write settings: %v", err)
		}
	})
	if !session.waitForOutput("v0.0.0", 10*time.Second) {
		t.Fatalf("pier did not start:\n%.600s", session.output())
	}
	if !session.waitForOutput("]11;?", 5*time.Second) {
		t.Fatalf("the OSC 11 probe was not written:\n%.600s", session.output())
	}
	// Answer the way a terminal would; the reply must be consumed, not editor
	// input, and must not freeze the loop.
	session.send("\x1b]11;#ffffff\x07")
	time.Sleep(300 * time.Millisecond)
	session.typeAndSubmit("hello after the background reply")
	time.Sleep(1500 * time.Millisecond)

	dump, exited := session.quitAndDump(5 * time.Second)
	if exited {
		t.Fatalf("pier exited before SIGQUIT after the reply; output:\n%.1500s", stripAnsiForLog(session.output()))
	}
	assertNoMutexBlocked(t, dump)
}

// TestPTYModelSelectorNoMutexDeadlock drives the D137 flow: open the model
// selector, move the selection, cancel.
func TestPTYModelSelectorNoMutexDeadlock(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	session := startFullscreenPier(t)
	if !session.waitForOutput("v0.0.0", 10*time.Second) {
		t.Fatalf("pier did not start:\n%.600s", session.output())
	}
	session.send("/model")
	time.Sleep(300 * time.Millisecond)
	session.send("\r")
	time.Sleep(1200 * time.Millisecond)
	session.send("\x1b[B")
	time.Sleep(200 * time.Millisecond)
	session.send("\x1b")
	time.Sleep(500 * time.Millisecond)

	dump, exited := session.quitAndDump(5 * time.Second)
	if exited {
		t.Fatal("pier exited before SIGQUIT; expected it to stay running after the selector flow")
	}
	assertNoMutexBlocked(t, dump)
}

// TestPTYScrollNoMutexDeadlock drives the D138 flow: fullscreen scrolling with
// wheel and page keys.
func TestPTYScrollNoMutexDeadlock(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	session := startFullscreenPier(t)
	if !session.waitForOutput("v0.0.0", 10*time.Second) {
		t.Fatalf("pier did not start:\n%.600s", session.output())
	}
	// Wheel up/down (SGR press + release) and page keys.
	for i := 0; i < 5; i++ {
		session.send("\x1b[<64;10;10M\x1b[<64;10;10m")
		time.Sleep(60 * time.Millisecond)
	}
	session.send("\x1b[5~") // PageUp
	time.Sleep(200 * time.Millisecond)
	session.send("\x1b[6~") // PageDown
	for i := 0; i < 5; i++ {
		session.send("\x1b[<65;10;10M\x1b[<65;10;10m")
		time.Sleep(60 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)

	dump, exited := session.quitAndDump(5 * time.Second)
	if exited {
		t.Fatal("pier exited before SIGQUIT; expected it to stay running after scrolling")
	}
	assertNoMutexBlocked(t, dump)
}

// TestPTYExitNoMutexDeadlock drives the D139 flow: /quit must exit cleanly and
// promptly; a hang is dumped and reported.
func TestPTYExitNoMutexDeadlock(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	session := startPier(t)
	if !session.waitForOutput("v0.0.0", 10*time.Second) {
		t.Fatalf("pier did not start:\n%.600s", session.output())
	}
	session.typeAndSubmit("/quit")

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if session.exited() {
			return // clean exit
		}
		time.Sleep(50 * time.Millisecond)
	}
	dump, _ := session.quitAndDump(3 * time.Second)
	assertNoMutexBlocked(t, dump)
	t.Fatalf("/quit did not exit within 5s (D139 regression); dump:\n%s", truncate(dump, 4000))
}

// TestDeadlockHelper is the re-exec'd child for TestMutexBlockedDetectorHasTeeth:
// it parks a goroutine on a mutex forever so SIGQUIT produces a stack dump with
// a mutex-blocked goroutine.
func TestDeadlockHelper(t *testing.T) {
	if os.Getenv("PIER_DEADLOCK_HELPER") == "" {
		t.Skip("helper process only")
	}
	var mu sync.Mutex
	mu.Lock()
	blocked := make(chan struct{})
	blocking := make(chan struct{})
	go func() {
		close(blocking)
		mu.Lock() // parks forever
		close(blocked)
	}()
	<-blocking
	// Ready once the goroutine is about to park; the parent waits for this
	// marker before dumping so the stack is always in the dump.
	fmt.Fprintln(os.Stderr, deadlockReadyMarker)
	<-blocked
}

// deadlockReadyMarker is printed by TestDeadlockHelper once its goroutine is
// about to park on the mutex.
const deadlockReadyMarker = "PIER_DEADLOCK_READY"

// TestMutexBlockedDetectorHasTeeth spawns a process that deadlocks on a mutex,
// SIGQUITs it, and asserts the watchdog's parser flags the dump. Without this
// the PTY flows could pass vacuously if dumping or parsing broke.
func TestMutexBlockedDetectorHasTeeth(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if runtime.GOOS == "windows" {
		t.Skip("requires unix signals")
	}
	master, slave, err := openPty()
	if err != nil {
		t.Skipf("pty unavailable: %v", err)
	}
	defer unix.Close(master)
	slaveFile := os.NewFile(uintptr(slave), "pty-slave")

	cmd := exec.Command(os.Args[0], "-test.run=TestDeadlockHelper")
	cmd.Env = append(os.Environ(), "PIER_DEADLOCK_HELPER=1")
	cmd.Stdin = slaveFile
	cmd.Stdout = slaveFile
	cmd.Stderr = slaveFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		t.Skipf("start helper: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()
	_ = slaveFile.Close()

	// Wait for the helper's goroutine to be parked, then dump it.
	ready := make([]byte, 0, 4096)
	buf := make([]byte, 65536)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(string(ready), deadlockReadyMarker) {
		n, err := unix.Read(master, buf)
		if n > 0 {
			ready = append(ready, buf[:n]...)
		}
		if err != nil {
			break
		}
	}
	if !strings.Contains(string(ready), deadlockReadyMarker) {
		t.Fatalf("helper never signalled readiness: %.300q", ready)
	}
	time.Sleep(200 * time.Millisecond) // let the goroutine park
	if err := cmd.Process.Signal(unix.SIGQUIT); err != nil {
		t.Fatalf("SIGQUIT: %v", err)
	}

	var dump bytes.Buffer
	dump.Write(ready)
	// SIGQUIT dumps the stacks and terminates the helper, so the read loop
	// ends on EIO after the dump is complete.
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		n, err := unix.Read(master, buf)
		if n > 0 {
			dump.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	text := dump.String()
	if !strings.Contains(text, "SIGQUIT: quit") {
		t.Fatalf("helper produced no dump: %.400q", text)
	}
	if !mutexBlockedRe.MatchString(text) {
		t.Fatalf("watchdog parser missed the mutex-blocked goroutine:\n%.1200s", text)
	}
}
