package coding

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tests ported from packages/coding-agent/test/resource-loader.test.ts:
// the context-file discovery cases and the nested-worktree dedup suite.

func writeContextFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func contextContents(files []ContextFile) []string {
	out := make([]string, 0, len(files))
	for _, file := range files {
		out = append(out, file.Content)
	}
	return out
}

func TestLoadContextFileFromDirPreference(t *testing.T) {
	dir := t.TempDir()
	if file := LoadContextFileFromDir(dir); file != nil {
		t.Fatalf("empty dir = %+v", file)
	}
	// AGENTS.override.md wins over AGENTS.md, which wins over CLAUDE.md.
	writeContextFile(t, filepath.Join(dir, "CLAUDE.md"), "claude")
	if file := LoadContextFileFromDir(dir); file == nil || file.Content != "claude" {
		t.Fatalf("file = %+v", file)
	}
	writeContextFile(t, filepath.Join(dir, "AGENTS.md"), "agents")
	if file := LoadContextFileFromDir(dir); file == nil || file.Content != "agents" {
		t.Fatalf("file = %+v", file)
	}
	writeContextFile(t, filepath.Join(dir, "AGENTS.override.md"), "override")
	if file := LoadContextFileFromDir(dir); file == nil || file.Content != "override" {
		t.Fatalf("file = %+v", file)
	}
	// A leading BOM is stripped.
	writeContextFile(t, filepath.Join(dir, "AGENTS.override.md"), "\ufeffwith bom")
	if file := LoadContextFileFromDir(dir); file == nil || file.Content != "with bom" {
		t.Fatalf("file = %+v", file)
	}
}

func TestLoadContextFileFromDirSkipsDirectories(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "AGENTS.override.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "AGENTS.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeContextFile(t, filepath.Join(dir, "CLAUDE.md"), "Fallback instructions")
	file := LoadContextFileFromDir(dir)
	if file == nil || file.Content != "Fallback instructions" {
		t.Fatalf("file = %+v", file)
	}
}

func TestLoadProjectContextFilesLayering(t *testing.T) {
	agentDir := t.TempDir()
	root := t.TempDir()
	cwd := filepath.Join(root, "project")
	nestedCwd := filepath.Join(cwd, "service")
	if err := os.MkdirAll(nestedCwd, 0o755); err != nil {
		t.Fatal(err)
	}

	// The global agent context file comes first, then ancestors outermost-first.
	writeContextFile(t, filepath.Join(agentDir, "AGENTS.md"), "global instructions")
	writeContextFile(t, filepath.Join(agentDir, "AGENTS.override.md"), "global override")
	writeContextFile(t, filepath.Join(cwd, "AGENTS.md"), "project instructions")
	writeContextFile(t, filepath.Join(nestedCwd, "AGENTS.md"), "service instructions")
	writeContextFile(t, filepath.Join(nestedCwd, "AGENTS.override.md"), "service override")

	files := LoadProjectContextFiles(nestedCwd, agentDir)
	got := contextContents(files)
	want := []string{"global override", "project instructions", "service override"}
	if len(got) != len(want) {
		t.Fatalf("contents = %#v", got)
	}
	for index, content := range want {
		if got[index] != content {
			t.Fatalf("contents = %#v, want %#v", got, want)
		}
	}
	if files[0].Path != filepath.Join(agentDir, "AGENTS.override.md") {
		t.Fatalf("global path = %s", files[0].Path)
	}
}

func TestLoadProjectContextFilesDedupesGlobal(t *testing.T) {
	// The agent dir nested under cwd must not duplicate its own context file.
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	cwd := filepath.Join(root, "project")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	writeContextFile(t, filepath.Join(root, "AGENTS.md"), "root instructions")
	writeContextFile(t, filepath.Join(agentDir, "AGENTS.md"), "global instructions")

	files := LoadProjectContextFiles(cwd, agentDir)
	got := contextContents(files)
	if len(got) != 2 || got[0] != "global instructions" || got[1] != "root instructions" {
		t.Fatalf("contents = %#v", got)
	}
}

// linkWorktree builds a linked-worktree skeleton without git: the main repo's
// .git/worktrees/<name>/ holds HEAD plus a commondir pointing back at the main
// .git, and the worktree carries a .git file whose gitdir: resolves to it.
func linkWorktree(t *testing.T, mainDir, worktreeDir, name string) {
	t.Helper()
	gitDir := filepath.Join(mainDir, ".git", "worktrees", name)
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeContextFile(t, filepath.Join(mainDir, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeContextFile(t, filepath.Join(gitDir, "HEAD"), "ref: refs/heads/feat\n")
	writeContextFile(t, filepath.Join(gitDir, "commondir"), "../..")
	if err := os.MkdirAll(worktreeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreeDir, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func setupNestedWorktree(t *testing.T) (main, worktree, worktreeSrc string) {
	t.Helper()
	outer := filepath.Join(t.TempDir(), "outer")
	main = filepath.Join(outer, "main")
	worktree = filepath.Join(main, "worktrees", "feat")
	worktreeSrc = filepath.Join(worktree, "src")
	if err := os.MkdirAll(worktreeSrc, 0o755); err != nil {
		t.Fatal(err)
	}
	linkWorktree(t, main, worktree, "feat")
	return main, worktree, worktreeSrc
}

func TestNestedWorktreeSkipsDuplicateContext(t *testing.T) {
	agentDir := t.TempDir()

	// The worktree's own context shadows the main repo's file.
	main, worktree, worktreeSrc := setupNestedWorktree(t)
	writeContextFile(t, filepath.Join(main, "AGENTS.md"), "main repo instructions")
	writeContextFile(t, filepath.Join(worktree, "AGENTS.md"), "worktree instructions")
	files := LoadProjectContextFiles(worktreeSrc, agentDir)
	if got := contextContents(files); len(got) != 1 || got[0] != "worktree instructions" {
		t.Fatalf("contents = %#v", got)
	}

	// Without a worktree copy the main repo's context is inherited.
	main, _, worktreeSrc = setupNestedWorktree(t)
	writeContextFile(t, filepath.Join(main, "AGENTS.md"), "main repo instructions")
	files = LoadProjectContextFiles(worktreeSrc, agentDir)
	if got := contextContents(files); len(got) != 1 || got[0] != "main repo instructions" {
		t.Fatalf("contents = %#v", got)
	}

	// Only the same filename is skipped.
	main, worktree, worktreeSrc = setupNestedWorktree(t)
	writeContextFile(t, filepath.Join(main, "CLAUDE.md"), "main repo instructions")
	writeContextFile(t, filepath.Join(worktree, "AGENTS.md"), "worktree instructions")
	files = LoadProjectContextFiles(worktreeSrc, agentDir)
	if got := contextContents(files); len(got) != 2 || got[0] != "main repo instructions" || got[1] != "worktree instructions" {
		t.Fatalf("contents = %#v", got)
	}

	// Ancestors above the main repo stay, with the duplicate dropped.
	outer := filepath.Dir(main)
	writeContextFile(t, filepath.Join(outer, "AGENTS.md"), "outer instructions")
	writeContextFile(t, filepath.Join(main, "AGENTS.md"), "main repo instructions")
	writeContextFile(t, filepath.Join(worktree, "AGENTS.md"), "worktree instructions")
	files = LoadProjectContextFiles(worktreeSrc, agentDir)
	if got := contextContents(files); len(got) != 2 || got[0] != "outer instructions" || got[1] != "worktree instructions" {
		t.Fatalf("contents = %#v", got)
	}
}

func TestNestedWorktreeBareLayoutKeepsContainerContext(t *testing.T) {
	agentDir := t.TempDir()
	proj := filepath.Join(t.TempDir(), "proj")
	bare := filepath.Join(proj, ".bare")
	worktree := filepath.Join(proj, "main")
	worktreeGitDir := filepath.Join(bare, "worktrees", "main")
	if err := os.MkdirAll(worktreeGitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	writeContextFile(t, filepath.Join(bare, "HEAD"), "ref: refs/heads/main\n")
	writeContextFile(t, filepath.Join(worktreeGitDir, "HEAD"), "ref: refs/heads/main\n")
	writeContextFile(t, filepath.Join(worktreeGitDir, "commondir"), "../..")
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+worktreeGitDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeContextFile(t, filepath.Join(proj, "AGENTS.md"), "container instructions")
	writeContextFile(t, filepath.Join(worktree, "AGENTS.md"), "worktree instructions")

	files := LoadProjectContextFiles(worktree, agentDir)
	got := contextContents(files)
	if len(got) != 2 || got[0] != "container instructions" || got[1] != "worktree instructions" {
		t.Fatalf("contents = %#v", got)
	}
}

func TestSiblingWorktreeAndSubmoduleKeepAncestors(t *testing.T) {
	agentDir := t.TempDir()

	// A sibling worktree: the main repo is not an ancestor, so nothing is skipped.
	outer := filepath.Join(t.TempDir(), "outer")
	main := filepath.Join(outer, "main")
	sibling := filepath.Join(outer, "sib-feat")
	siblingSrc := filepath.Join(sibling, "src")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(siblingSrc, 0o755); err != nil {
		t.Fatal(err)
	}
	writeContextFile(t, filepath.Join(outer, "AGENTS.md"), "outer instructions")
	writeContextFile(t, filepath.Join(sibling, "AGENTS.md"), "sibling worktree instructions")
	linkWorktree(t, main, sibling, "sib")
	files := LoadProjectContextFiles(siblingSrc, agentDir)
	if got := contextContents(files); len(got) != 2 || got[0] != "outer instructions" || got[1] != "sibling worktree instructions" {
		t.Fatalf("contents = %#v", got)
	}

	// A submodule's gitdir has no commondir, so the superproject's context stays.
	super := filepath.Join(t.TempDir(), "super")
	submodule := filepath.Join(super, "vendor", "lib")
	submoduleSrc := filepath.Join(submodule, "src")
	if err := os.MkdirAll(submoduleSrc, 0o755); err != nil {
		t.Fatal(err)
	}
	writeContextFile(t, filepath.Join(super, "AGENTS.md"), "superproject instructions")
	writeContextFile(t, filepath.Join(submodule, "AGENTS.md"), "submodule instructions")
	subGitDir := filepath.Join(super, ".git", "modules", "vendor", "lib")
	if err := os.MkdirAll(subGitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeContextFile(t, filepath.Join(subGitDir, "HEAD"), "ref: refs/heads/main\n")
	if err := os.WriteFile(filepath.Join(submodule, ".git"), []byte("gitdir: "+subGitDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files = LoadProjectContextFiles(submoduleSrc, agentDir)
	if got := contextContents(files); len(got) != 2 || got[0] != "superproject instructions" || got[1] != "submodule instructions" {
		t.Fatalf("contents = %#v", got)
	}
}

func TestOrdinaryRepoAndMissingGitdirClimbNormally(t *testing.T) {
	agentDir := t.TempDir()

	// An ordinary repository root does not stop the climb.
	outer := filepath.Join(t.TempDir(), "outer")
	repo := filepath.Join(outer, "repo")
	leaf := filepath.Join(repo, "src")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(leaf, 0o755); err != nil {
		t.Fatal(err)
	}
	writeContextFile(t, filepath.Join(repo, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeContextFile(t, filepath.Join(outer, "AGENTS.md"), "outer instructions")
	writeContextFile(t, filepath.Join(repo, "AGENTS.md"), "repo instructions")
	writeContextFile(t, filepath.Join(leaf, "AGENTS.md"), "leaf instructions")
	files := LoadProjectContextFiles(leaf, agentDir)
	got := contextContents(files)
	if len(got) != 3 || got[0] != "outer instructions" || got[1] != "repo instructions" || got[2] != "leaf instructions" {
		t.Fatalf("contents = %#v", got)
	}

	// A dangling gitdir: target is ignored and the climb continues.
	corrupt := filepath.Join(t.TempDir(), "corrupt")
	src := filepath.Join(corrupt, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(corrupt, ".git"), []byte("gitdir: /nonexistent/path/worktrees/feat\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeContextFile(t, filepath.Join(corrupt, "AGENTS.md"), "repo instructions")
	writeContextFile(t, filepath.Join(src, "AGENTS.md"), "src instructions")
	files = LoadProjectContextFiles(src, agentDir)
	got = contextContents(files)
	if len(got) != 2 || got[0] != "repo instructions" || got[1] != "src instructions" {
		t.Fatalf("contents = %#v", got)
	}
}

func TestFindGitPaths(t *testing.T) {
	// An ordinary repository: .git is a directory with HEAD.
	repo := filepath.Join(t.TempDir(), "repo")
	leaf := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(leaf, 0o755); err != nil {
		t.Fatal(err)
	}
	// HEAD is required: a .git directory without it is not a repository.
	writeContextFile(t, filepath.Join(repo, ".git", "HEAD"), "ref: refs/heads/main\n")
	paths := FindGitPaths(leaf)
	if paths == nil {
		t.Fatal("expected git paths")
	}
	if paths.RepoDir != repo || paths.CommonGitDir != filepath.Join(repo, ".git") ||
		paths.HeadPath != filepath.Join(repo, ".git", "HEAD") {
		t.Fatalf("paths = %+v", paths)
	}
	if FindGitPaths(t.TempDir()) != nil {
		t.Fatal("a directory without git metadata must return nil")
	}

	// A worktree: the .git file's gitdir resolves through commondir.
	main, worktree, worktreeSrc := setupNestedWorktree(t)
	paths = FindGitPaths(worktreeSrc)
	if paths == nil || paths.RepoDir != worktree {
		t.Fatalf("paths = %+v", paths)
	}
	if paths.CommonGitDir != filepath.Join(main, ".git") {
		t.Fatalf("commonGitDir = %s", paths.CommonGitDir)
	}
	// Without a worktree context file nothing is shadowed.
	if got := FindShadowedContextFile(worktreeSrc); got != nil {
		t.Fatalf("shadowed = %v", got)
	}
	// The worktree's own context file shadows the main repo's same-named file.
	writeContextFile(t, filepath.Join(worktree, "AGENTS.md"), "worktree")
	shadowed := FindShadowedContextFile(worktreeSrc)
	if shadowed == nil || *shadowed != filepath.Join(main, "AGENTS.md") {
		t.Fatalf("shadowed = %v", shadowed)
	}
	// A sibling worktree has no shadowed file: the main repo is not an ancestor.
	sibling := filepath.Join(filepath.Dir(main), "sibling")
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	writeContextFile(t, filepath.Join(sibling, "AGENTS.md"), "sibling")
	linkWorktree(t, main, sibling, "sib")
	if got := FindShadowedContextFile(sibling); got != nil {
		t.Fatalf("sibling shadowed = %v", got)
	}
}

// TestBuildSystemPromptIncludesContextFiles pins the wiring the CLI boot
// uses: the global agent-dir context file and the workspace ancestor files
// land in the rendered system prompt (upstream agent-session._buildRuntime
// feeds resource-loader agentsFiles into the prompt options).
func TestBuildSystemPromptIncludesContextFiles(t *testing.T) {
	dir := t.TempDir()
	agentDir := filepath.Join(dir, "agent")
	if err := os.MkdirAll(filepath.Join(agentDir, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "AGENTS.md"), []byte("GLOBAL-CONTEXT-MARKER"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("WORKSPACE-CONTEXT-MARKER"), 0o644); err != nil {
		t.Fatal(err)
	}

	contextFiles := LoadProjectContextFiles(dir, agentDir)
	if len(contextFiles) != 2 {
		t.Fatalf("context files = %d, want 2", len(contextFiles))
	}
	prompt, err := BuildSystemPrompt(BuildSystemPromptOptions{
		Cwd: dir, ContextFiles: contextFiles, SelectedTools: []string{"read", "bash"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "GLOBAL-CONTEXT-MARKER") || !strings.Contains(prompt, "WORKSPACE-CONTEXT-MARKER") {
		t.Fatal("context file contents missing from the system prompt")
	}
}
