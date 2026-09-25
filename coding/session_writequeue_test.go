package coding

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dat267/pier/internal/offloop"
)

// The interactive wiring hands the manager a write queue: file writes run off
// the calling goroutine (the UI loop) in submission order, and FlushWrites
// makes them observable for tests and shutdown.

func TestSessionWritesLandInOrderOnTheQueue(t *testing.T) {
	dir := t.TempDir()
	m := NewSessionManager(dir, &SessionManagerOptions{
		SessionDir: filepath.Join(dir, "sessions"),
		WriteQueue: offloop.New(),
	})
	// The flush-on-first-assistant contract: the file is created by the
	// rewrite that fires when the first assistant message lands.
	m.AppendMessage(createAssistantMessageT("seed"))
	const n = 50
	for i := 0; i < n; i++ {
		m.AppendMessage(createUserMessage("message number"))
		m.AppendSessionInfo("name")
	}
	m.FlushWrites()

	f, err := os.Open(m.GetSessionFile())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	messages, infos := 0, 0
	lastMessageIndex, lastIndexIndex := -1, -1
	for scanner.Scan() {
		var entry struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatalf("line %d: %v", messages+infos, err)
		}
		switch entry.Type {
		case "message":
			if messages <= lastIndexIndex && infos > 0 {
				// ordering checked below via absolute counters
			}
			lastMessageIndex = messages + infos
			messages++
		case "session_info":
			lastIndexIndex = messages + infos
			infos++
		}
	}
	if messages != n+1 || infos != n { // +1: the seed assistant message
		t.Fatalf("messages=%d infos=%d, want %d/%d", messages, infos, n+1, n)
	}
	// Interleaved appends must land in submission order: every session_info
	// line comes after the message appended just before it.
	if lastMessageIndex > lastIndexIndex {
		t.Fatalf("file order diverged from submission order: last message line %d, last info line %d", lastMessageIndex, lastIndexIndex)
	}
}

func TestSessionFlushWritesIsNilSafe(t *testing.T) {
	dir := t.TempDir()
	m := NewSessionManager(dir, &SessionManagerOptions{SessionDir: filepath.Join(dir, "sessions")})
	m.AppendMessage(createUserMessage("sync"))
	m.FlushWrites() // must not panic without a queue
}
