package coding

import "testing"

// TestSessionManagerCachesBranch pins that GetBranch("") reuses one walk: the
// footer asks for context usage on every paint, and rebuilding the branch of a
// long session is O(entries) (12.6 ms on a 15.5k-entry session).
func TestSessionManagerCachesBranch(t *testing.T) {
	m, _ := newTestSession(t)
	m.AppendMessage(createUserMessage("one"))
	m.AppendMessage(createUserMessage("two"))

	first := m.GetBranch("")
	second := m.GetBranch("")
	if len(first) == 0 {
		t.Fatal("empty branch")
	}
	if &first[0] != &second[0] {
		t.Fatal("GetBranch rebuilt the branch instead of reusing the cached walk")
	}
	// The shared result must not have spare capacity: a caller appending to it
	// would write into the cache's backing array.
	if cap(first) != len(first) {
		t.Fatalf("cached branch cap = %d, want %d", cap(first), len(first))
	}

	// A new entry invalidates the cache.
	m.AppendMessage(createUserMessage("three"))
	third := m.GetBranch("")
	if len(third) != len(first)+1 {
		t.Fatalf("branch after append = %d entries, want %d", len(third), len(first)+1)
	}
	if &third[0] == &first[0] {
		t.Fatal("append did not invalidate the cached branch")
	}

	// A leaf move invalidates the cache too.
	tree := m.GetEntries()
	if len(tree) >= 2 {
		target := tree[0].ID
		path := m.GetBranch(target)
		if len(path) == 0 {
			t.Fatal("explicit branch is empty")
		}
	}
}

// TestSessionManagerBranchCacheSurvivesFromID checks the cached branch is the
// current leaf's, not an explicitly requested one.
func TestSessionManagerBranchCacheSurvivesFromID(t *testing.T) {
	m, _ := newTestSession(t)
	m.AppendMessage(createUserMessage("one"))
	m.AppendMessage(createUserMessage("two"))
	entries := m.GetEntries()
	firstID := entries[0].ID

	cachedLeaf := m.GetBranch("")
	explicit := m.GetBranch(firstID)
	if len(explicit) >= len(cachedLeaf) {
		t.Fatalf("explicit branch %d entries, leaf branch %d", len(explicit), len(cachedLeaf))
	}
	// The explicit walk must not have replaced the leaf cache.
	again := m.GetBranch("")
	if &again[0] != &cachedLeaf[0] {
		t.Fatal("explicit GetBranch(fromID) clobbered the leaf cache")
	}
}
