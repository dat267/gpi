package coding

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Port of core/footer-data-provider.ts: the footer data source (git branch,
// extension statuses, available provider count) used by the interactive mode.
//
// Divergences: upstream uses fs.watch/fs.watchFile (with WSL polling); the Go
// port polls HEAD on a ticker instead (D107, same approach as the theme
// watcher D75). The branch resolution is synchronous inside the poll
// goroutine instead of the upstream async exec.

// FooterDataProviderOptions configure the provider.
type FooterDataProviderOptions struct {
	// PollIntervalMS overrides the HEAD poll interval (default 500ms). Zero
	// uses the default; negative disables watching.
	PollIntervalMS int
	// DisableWatch turns off the poll watcher (tests).
	DisableWatch bool
}

// FooterDataProvider provides git branch and extension statuses.
type FooterDataProvider struct {
	mu sync.Mutex

	cwd       string
	gitPaths  *GitPaths
	branch    *string // nil = unresolved; "" = detached (upstream null)
	resolved  bool
	statuses  map[string]string
	providers int
	callbacks map[int]func()
	nextCB    int
	disposed  bool

	stopWatch chan struct{}
	watchDone chan struct{}
}

// NewFooterDataProvider creates the provider for a working directory.
func NewFooterDataProvider(cwd string, options FooterDataProviderOptions) *FooterDataProvider {
	provider := &FooterDataProvider{
		cwd:       cwd,
		gitPaths:  FindGitPaths(cwd),
		statuses:  map[string]string{},
		callbacks: map[int]func(){},
	}
	if !options.DisableWatch {
		interval := 500 * time.Millisecond
		if options.PollIntervalMS != 0 {
			interval = time.Duration(options.PollIntervalMS) * time.Millisecond
		}
		if interval > 0 {
			provider.stopWatch = make(chan struct{})
			provider.watchDone = make(chan struct{})
			go provider.watchLoop(provider.stopWatch, provider.watchDone, interval)
		}
	}
	return provider
}

// Cwd returns the provider's working directory.
func (f *FooterDataProvider) Cwd() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cwd
}

// GetGitBranch returns the current branch, "" for detached HEAD, or nil when
// not in a repository (the bool is false for nil).
func (f *FooterDataProvider) GetGitBranch() (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.resolved {
		branch, ok := f.resolveGitBranch()
		if !ok {
			f.branch = nil
		} else {
			f.branch = &branch
		}
		f.resolved = true
	}
	if f.branch == nil {
		return "", false
	}
	return *f.branch, true
}

// GetExtensionStatuses returns a copy of the extension status texts.
func (f *FooterDataProvider) GetExtensionStatuses() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	copyOf := make(map[string]string, len(f.statuses))
	for key, value := range f.statuses {
		copyOf[key] = value
	}
	return copyOf
}

// OnBranchChange subscribes to git branch changes.
func (f *FooterDataProvider) OnBranchChange(callback func()) func() {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.nextCB
	f.nextCB++
	f.callbacks[id] = callback
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(f.callbacks, id)
	}
}

// SetExtensionStatus sets or clears an extension status text.
func (f *FooterDataProvider) SetExtensionStatus(key string, text *string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if text == nil {
		delete(f.statuses, key)
		return
	}
	f.statuses[key] = *text
}

// ClearExtensionStatuses clears all extension status texts.
func (f *FooterDataProvider) ClearExtensionStatuses() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses = map[string]string{}
}

// GetAvailableProviderCount returns the provider count shown in the footer.
func (f *FooterDataProvider) GetAvailableProviderCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.providers
}

// SetAvailableProviderCount updates the provider count.
func (f *FooterDataProvider) SetAvailableProviderCount(count int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.providers = count
}

// SetCwd changes the working directory and re-resolves the repository.
func (f *FooterDataProvider) SetCwd(cwd string) {
	f.mu.Lock()
	if f.cwd == cwd {
		f.mu.Unlock()
		return
	}
	f.cwd = cwd
	f.resolved = false
	f.branch = nil
	f.gitPaths = FindGitPaths(cwd)
	f.mu.Unlock()
	f.notifyBranchChange()
}

// Dispose stops the watcher and clears the callbacks.
func (f *FooterDataProvider) Dispose() {
	f.mu.Lock()
	if f.disposed {
		f.mu.Unlock()
		return
	}
	f.disposed = true
	stopWatch := f.stopWatch
	watchDone := f.watchDone
	f.stopWatch = nil
	f.watchDone = nil
	f.callbacks = map[int]func(){}
	f.mu.Unlock()
	if stopWatch != nil {
		close(stopWatch)
		<-watchDone
	}
}

func (f *FooterDataProvider) notifyBranchChange() {
	f.mu.Lock()
	callbacks := make([]func(), 0, len(f.callbacks))
	for _, callback := range f.callbacks {
		callbacks = append(callbacks, callback)
	}
	f.mu.Unlock()
	for _, callback := range callbacks {
		callback()
	}
}

// Refresh re-resolves the branch and notifies on change. It is exported for
// tests and for hosts that manage their own watcher.
func (f *FooterDataProvider) Refresh() {
	f.mu.Lock()
	if f.disposed {
		f.mu.Unlock()
		return
	}
	next, ok := f.resolveGitBranch()
	previous := f.branch
	if !ok {
		f.branch = nil
	} else {
		f.branch = &next
	}
	f.resolved = true
	changed := previous != nil && (f.branch == nil || *previous != next)
	f.mu.Unlock()
	if changed {
		f.notifyBranchChange()
	}
}

// watchLoop polls the git head. The channels are passed in because Dispose
// nils the fields (a double dispose must not close a nil channel).
func (f *FooterDataProvider) watchLoop(stopWatch, watchDone chan struct{}, interval time.Duration) {
	defer close(watchDone)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	lastHead := f.readHeadContent()
	for {
		select {
		case <-stopWatch:
			return
		case <-ticker.C:
			current := f.readHeadContent()
			if current == lastHead {
				continue
			}
			lastHead = current
			f.Refresh()
		}
	}
}

func (f *FooterDataProvider) readHeadContent() string {
	f.mu.Lock()
	gitPaths := f.gitPaths
	f.mu.Unlock()
	if gitPaths == nil {
		return ""
	}
	content, err := os.ReadFile(gitPaths.HeadPath)
	if err != nil {
		return ""
	}
	return string(content)
}

// resolveGitBranch resolves the branch from HEAD (caller holds the lock).
func (f *FooterDataProvider) resolveGitBranch() (string, bool) {
	if f.gitPaths == nil {
		return "", false
	}
	content, err := os.ReadFile(f.gitPaths.HeadPath)
	if err != nil {
		return "", false
	}
	trimmed := strings.TrimSpace(string(content))
	if strings.HasPrefix(trimmed, "ref: refs/heads/") {
		branch := trimmed[len("ref: refs/heads/"):]
		if branch == ".invalid" {
			if resolved := ResolveGitBranch(f.gitPaths.RepoDir); resolved != "" {
				return resolved, true
			}
			return "detached", true
		}
		return branch, true
	}
	return "detached", true
}

// ReadonlyFooterDataProvider is the extension-facing read-only view.
type ReadonlyFooterDataProvider interface {
	GetGitBranch() (string, bool)
	GetExtensionStatuses() map[string]string
	GetAvailableProviderCount() int
	OnBranchChange(callback func()) func()
}

var _ ReadonlyFooterDataProvider = (*FooterDataProvider)(nil)

// GitHeadDirectory returns the directory containing HEAD (used by hosts that
// watch git metadata themselves).
func GitHeadDirectory(gitPaths *GitPaths) string {
	if gitPaths == nil {
		return ""
	}
	return filepath.Dir(gitPaths.HeadPath)
}
