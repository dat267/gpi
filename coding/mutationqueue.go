package coding

import (
	"path/filepath"
	"sync"
)

// Port of core/tools/file-mutation-queue.ts: serialize file mutations
// targeting the same file; operations for different files run in parallel.
// Upsteram keys queues by realpath (falling back to the resolved path for
// missing files).

var (
	mutationQueues   map[string]*sync.Mutex
	mutationQueuesMu sync.Mutex
)

func init() {
	mutationQueues = map[string]*sync.Mutex{}
}

// getMutationQueueKey resolves the queue key by realpath, falling back to
// the resolved path for missing files.
func getMutationQueueKey(filePath string) string {
	resolved := filepath.Clean(filePath)
	if realpath, err := filepath.EvalSymlinks(resolved); err == nil {
		return realpath
	}
	return resolved
}

// WithFileMutationQueue serializes file mutation operations targeting the
// same file.
func WithFileMutationQueue(filePath string, fn func() error) error {
	key := getMutationQueueKey(filePath)
	mutationQueuesMu.Lock()
	mu, ok := mutationQueues[key]
	if !ok {
		mu = &sync.Mutex{}
		mutationQueues[key] = mu
	}
	mutationQueuesMu.Unlock()

	mu.Lock()
	defer mu.Unlock()
	return fn()
}
