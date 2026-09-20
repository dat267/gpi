package coding

import (
	"strings"
	"testing"
)

// Round 118 tests: session resource cleanups.

func TestSessionResourceCleanups(t *testing.T) {
	var cleaned []string
	unregisterOne := RegisterSessionResourceCleanup(func(sessionID string) {
		cleaned = append(cleaned, "one:"+sessionID)
	})
	defer unregisterOne()
	unregisterTwo := RegisterSessionResourceCleanup(func(sessionID string) {
		cleaned = append(cleaned, "two:"+sessionID)
	})
	defer unregisterTwo()

	if err := CleanupSessionResources("session-1"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(cleaned, ",") != "one:session-1,two:session-1" {
		t.Fatalf("cleaned = %v", cleaned)
	}

	// Unregistration removes only that cleanup.
	unregisterTwo()
	cleaned = nil
	if err := CleanupSessionResources("session-2"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(cleaned, ",") != "one:session-2" {
		t.Fatalf("cleaned = %v", cleaned)
	}

	// A panicking cleanup is reported without stopping the others.
	unregisterBoom := RegisterSessionResourceCleanup(func(sessionID string) { panic("boom") })
	defer unregisterBoom()
	cleaned = nil
	err := CleanupSessionResources("session-3")
	if err == nil || !strings.Contains(err.Error(), "Failed to cleanup session resources") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	if strings.Join(cleaned, ",") != "one:session-3" {
		t.Fatalf("cleaned = %v", cleaned)
	}
}
