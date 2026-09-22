package coding

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSkill(t *testing.T, dir string, name string, description string) string {
	t.Helper()
	skillDir := filepath.Join(dir, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(skillDir, "SKILL.md")
	content := "---\nname: " + name + "\ndescription: " + description + "\n---\nBody\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestLoadSkillsDedupesDefaultsAndPaths covers upstream loadSkills' real-path
// dedupe: the default agent-dir skills dir and a settings skill path pointing
// at the same directory (e.g. ~/.pi/agent/skills) must load each skill once,
// with no diagnostic. The port appended both, so every skill appeared twice.
func TestLoadSkillsDedupesDefaultsAndPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	agentDir := filepath.Join(home, ".pi", "agent")
	writeSkill(t, filepath.Join(agentDir, "skills"), "tdd", "TDD")

	result := LoadSkills(LoadSkillsOptions{
		Cwd: t.TempDir(), AgentDir: agentDir,
		SkillPaths: []string{"~/.pi/agent/skills"}, IncludeDefaults: true,
	}, false)

	if len(result.Skills) != 1 || result.Skills[0].Name != "tdd" {
		t.Fatalf("skills = %+v, want exactly one tdd", result.Skills)
	}
	for _, diagnostic := range result.Diagnostics {
		t.Fatalf("unexpected diagnostic: %+v", diagnostic)
	}
}

// TestLoadSkillsCollisionDiagnostic covers upstream's name-collision warning:
// two skills with the same name keep the first and record the loser.
func TestLoadSkillsCollisionDiagnostic(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	winner := writeSkill(t, dirA, "dup", "first")
	loser := writeSkill(t, dirB, "dup", "second")

	result := LoadSkills(LoadSkillsOptions{
		AgentDir: t.TempDir(), SkillPaths: []string{dirA, dirB},
	}, false)

	if len(result.Skills) != 1 || result.Skills[0].FilePath != winner {
		t.Fatalf("skills = %+v, want only %s", result.Skills, winner)
	}
	var collision *ResourceDiagnostic
	for index := range result.Diagnostics {
		if result.Diagnostics[index].Type == "collision" {
			collision = &result.Diagnostics[index]
		}
	}
	if collision == nil {
		t.Fatalf("missing collision diagnostic: %+v", result.Diagnostics)
	}
	if collision.Collision == nil || collision.Collision.WinnerPath != winner || collision.Collision.LoserPath != loser {
		t.Fatalf("collision = %+v", collision.Collision)
	}
}

// TestLoadSkillsResolvesTildePath covers upstream's resolvePath on skill paths:
// a "~" path resolves instead of producing a "does not exist" warning.
func TestLoadSkillsResolvesTildePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	skillsDir := filepath.Join(home, "myskills")
	writeSkill(t, skillsDir, "custom", "custom")

	result := LoadSkills(LoadSkillsOptions{
		AgentDir: t.TempDir(), SkillPaths: []string{"~/myskills"},
	}, false)

	if len(result.Skills) != 1 || result.Skills[0].Name != "custom" {
		t.Fatalf("skills = %+v", result.Skills)
	}
	if result.Skills[0].SourceInfo.Scope != SourceScopeTemporary {
		t.Fatalf("scope = %q, want temporary", result.Skills[0].SourceInfo.Scope)
	}
}
