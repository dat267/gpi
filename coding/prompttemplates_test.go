package coding

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Tests ported from packages/coding-agent/test/prompt-templates.test.ts.

func TestSubstituteArgsSimplePlaceholders(t *testing.T) {
	cases := []struct {
		template string
		args     []string
		want     string
	}{
		{"Test: $ARGUMENTS", []string{"a", "b", "c"}, "Test: a b c"},
		{"Test: $@", []string{"a", "b", "c"}, "Test: a b c"},
		{"$1: $ARGUMENTS", []string{"prefix", "a", "b"}, "prefix: prefix a b"},
		{"$1: $@", []string{"prefix", "a", "b"}, "prefix: prefix a b"},
		{"Test: $ARGUMENTS", nil, "Test: "},
		{"Test: $@", nil, "Test: "},
		{"Test: $1", nil, "Test: "},
		{"$ARGUMENTS and $ARGUMENTS", []string{"a", "b"}, "a b and a b"},
		{"$@ and $@", []string{"a", "b"}, "a b and a b"},
		{"$@ and $ARGUMENTS", []string{"a", "b"}, "a b and a b"},
		{"$1 $2: $ARGUMENTS", []string{"arg100", "@user"}, "arg100 @user: arg100 @user"},
		{"$1 $2 $3 $4 $5", []string{"a", "b"}, "a b   "},
		{"$ARGUMENTS", []string{"日本語", "🎉", "café"}, "日本語 🎉 café"},
		{"$1 $2", []string{"line1\nline2", "tab\tthere"}, "line1\nline2 tab\tthere"},
		{"$1$2", []string{"a", "b"}, "ab"},
		{"$ARGUMENTS", []string{"first arg", "second arg"}, "first arg second arg"},
		{"Test: $ARGUMENTS", []string{"only"}, "Test: only"},
		{"Test: $@", []string{"only"}, "Test: only"},
		{"$0", []string{"a", "b"}, ""},
		{"$1.5", []string{"a"}, "a.5"},
		{"pre$ARGUMENTS", []string{"a", "b"}, "prea b"},
		{"pre$@", []string{"a", "b"}, "prea b"},
		{"$ARGUMENTS", []string{"a", "", "c"}, "a  c"},
		{"$ARGUMENTS", []string{"  leading  ", "trailing  "}, "  leading   trailing  "},
		{"Prefix $ARGUMENTS suffix", []string{"ARGUMENTS"}, "Prefix ARGUMENTS suffix"},
		{"$A $$ $ $ARGS", []string{"a"}, "$A $$ $ $ARGS"},
		{"$arguments $Arguments $ARGUMENTS", []string{"a", "b"}, "$arguments $Arguments a b"},
		{"$1 $2 $3", []string{"a", "b", "c"}, "a b c"},
		{"$1: $@ ($ARGUMENTS)", []string{"first", "second", "third"}, "first: first second third (first second third)"},
		{"Just plain text", []string{"a", "b"}, "Just plain text"},
		{"$1 $2 $@", []string{"a", "b", "c"}, "a b a b c"},
	}
	for _, testCase := range cases {
		if got := SubstituteArgs(testCase.template, testCase.args); got != testCase.want {
			t.Errorf("SubstituteArgs(%q, %#v) = %q, want %q", testCase.template, testCase.args, got, testCase.want)
		}
	}

	// Argument values containing patterns stay literal (no recursion).
	if got := SubstituteArgs("$ARGUMENTS", []string{"$1", "$ARGUMENTS"}); got != "$1 $ARGUMENTS" {
		t.Fatalf("recursive substitution: %q", got)
	}
	if got := SubstituteArgs("$@", []string{"$100", "$1"}); got != "$100 $1" {
		t.Fatalf("recursive substitution: %q", got)
	}
	// Multiple-digit placeholders index correctly.
	args := make([]string, 15)
	for index := range args {
		args[index] = fmt.Sprintf("val%d", index)
	}
	if got := SubstituteArgs("$10 $12 $15", args); got != "val9 val11 val14" {
		t.Fatalf("multi-digit = %q", got)
	}
	// There is no escape mechanism: the backslash is literal.
	if got := SubstituteArgs(`Price: \$100`, nil); got != `Price: \` {
		t.Fatalf("escaped dollar = %q", got)
	}
	// Very long argument lists.
	long := make([]string, 100)
	for index := range long {
		long[index] = fmt.Sprintf("arg%d", index)
	}
	if got := SubstituteArgs("$ARGUMENTS", long); got != strings.Join(long, " ") {
		t.Fatalf("long list = %q", got)
	}
}

func TestSubstituteArgsDefaults(t *testing.T) {
	cases := []struct {
		template string
		args     []string
		want     string
	}{
		{`List exactly ${1:-7} next steps`, nil, "List exactly 7 next steps"},
		{`List exactly ${1:-7} next steps`, []string{"3"}, "List exactly 3 next steps"},
		{`Mode: ${1:-brief}`, []string{""}, "Mode: brief"},
		{`${1:-7} ${2:-brief}`, nil, "7 brief"},
		{`${1:-7} ${2:-brief}`, []string{"3"}, "3 brief"},
		{`${1:-7} ${2:-brief}`, []string{"3", "verbose"}, "3 verbose"},
		{`${1:-seven steps}`, nil, "seven steps"},
		{`${3:-fallback}`, []string{"a", "b"}, "fallback"},
		{`$1 ${2:-x} $ARGUMENTS`, []string{"a"}, "a x a"},
		{`${1:-7}`, []string{"$ARGUMENTS"}, "$ARGUMENTS"},
		{`${1:-7}`, []string{"$1"}, "$1"},
		{`${1:-$ARGUMENTS}`, []string{"a", "b"}, "a"},
		{`${3:-$ARGUMENTS}`, []string{"a", "b"}, "$ARGUMENTS"},
	}
	for _, testCase := range cases {
		if got := SubstituteArgs(testCase.template, testCase.args); got != testCase.want {
			t.Errorf("SubstituteArgs(%q, %#v) = %q, want %q", testCase.template, testCase.args, got, testCase.want)
		}
	}

	// Defaults for all arguments.
	template := `${@:-default}` + "\n" + `${ARGUMENTS:-default}`
	if got := SubstituteArgs(template, nil); got != "default\ndefault" {
		t.Fatalf("all-args default = %q", got)
	}
	if got := SubstituteArgs(template, []string{"This", "would", "be", "the", "arguments"}); got != "This would be the arguments\nThis would be the arguments" {
		t.Fatalf("all-args value = %q", got)
	}
}

func TestSubstituteArgsSlicing(t *testing.T) {
	cases := []struct {
		template string
		args     []string
		want     string
	}{
		{`${@:2}`, []string{"a", "b", "c", "d"}, "b c d"},
		{`${@:1}`, []string{"a", "b", "c"}, "a b c"},
		{`${@:3}`, []string{"a", "b", "c", "d"}, "c d"},
		{`${@:2:2}`, []string{"a", "b", "c", "d"}, "b c"},
		{`${@:1:1}`, []string{"a", "b", "c"}, "a"},
		{`${@:3:1}`, []string{"a", "b", "c", "d"}, "c"},
		{`${@:2:3}`, []string{"a", "b", "c", "d", "e"}, "b c d"},
		{`${@:99}`, []string{"a", "b"}, ""},
		{`${@:5}`, []string{"a", "b"}, ""},
		{`${@:10:5}`, []string{"a", "b"}, ""},
		{`${@:2:0}`, []string{"a", "b", "c"}, ""},
		{`${@:1:0}`, []string{"a", "b"}, ""},
		{`${@:2:99}`, []string{"a", "b", "c"}, "b c"},
		{`${@:1:10}`, []string{"a", "b"}, "a b"},
		{`${@:2} vs $@`, []string{"a", "b", "c"}, "b c vs a b c"},
		{`First: ${@:1:1}, All: $@`, []string{"x", "y", "z"}, "First: x, All: x y z"},
		{`$1: ${@:2}`, []string{"cmd", "arg1", "arg2"}, "cmd: arg1 arg2"},
		{`$1 $2 ${@:3}`, []string{"a", "b", "c", "d"}, "a b c d"},
		{`${@:0}`, []string{"a", "b", "c"}, "a b c"},
		{`${@:2}`, nil, ""},
		{`${@:1}`, nil, ""},
		{`${@:1}`, []string{"only"}, "only"},
		{`${@:2}`, []string{"only"}, ""},
		{`Process ${@:2} with $1`, []string{"tool", "file1", "file2"}, "Process file1 file2 with tool"},
		{`${@:1:1} and ${@:2}`, []string{"a", "b", "c"}, "a and b c"},
		{`${@:1:2} vs ${@:3:2}`, []string{"a", "b", "c", "d", "e"}, "a b vs c d"},
		{`${@:2}`, []string{"cmd", "first arg", "second arg"}, "first arg second arg"},
		{`${@:2}`, []string{"cmd", "$100", "@user", "#tag"}, "$100 @user #tag"},
		{`${@:1}`, []string{"日本語", "🎉", "café"}, "日本語 🎉 café"},
		{`prefix${@:2}suffix`, []string{"a", "b", "c"}, "prefixb csuffix"},
		{`Run $1 on ${@:2:2}, then process $@`, []string{"eslint", "file1.ts", "file2.ts", "file3.ts"},
			"Run eslint on file1.ts file2.ts, then process eslint file1.ts file2.ts file3.ts"},
	}
	for _, testCase := range cases {
		if got := SubstituteArgs(testCase.template, testCase.args); got != testCase.want {
			t.Errorf("SubstituteArgs(%q, %#v) = %q, want %q", testCase.template, testCase.args, got, testCase.want)
		}
	}

	// Slice patterns inside argument values stay literal.
	if got := SubstituteArgs(`${@:1}`, []string{"${@:2}", "test"}); got != "${@:2} test" {
		t.Fatalf("slice recursion = %q", got)
	}
	if got := SubstituteArgs(`${@:2}`, []string{"a", "${@:3}", "c"}); got != "${@:3} c" {
		t.Fatalf("slice recursion = %q", got)
	}
	// Large slice lengths clamp.
	args := make([]string, 10)
	for index := range args {
		args[index] = fmt.Sprintf("arg%d", index+1)
	}
	if got := SubstituteArgs(`${@:5:100}`, args); got != "arg5 arg6 arg7 arg8 arg9 arg10" {
		t.Fatalf("large slice = %q", got)
	}
}

func TestParseCommandArgsQuoting(t *testing.T) {
	cases := []struct {
		input string
		want  []string
	}{
		{"a b c", []string{"a", "b", "c"}},
		{`"first arg" second`, []string{"first arg", "second"}},
		{`'single quoted' arg`, []string{"single quoted", "arg"}},
		{`"double" 'single' plain`, []string{"double", "single", "plain"}},
		{"", nil},
		{"  extra   spaces  ", []string{"extra", "spaces"}},
		{"tab\tseparated", []string{"tab", "separated"}},
		{`"" " "`, []string{" "}},
		{`a@b.com #tag $var`, []string{"a@b.com", "#tag", "$var"}},
		{"日本語 🎉 café", []string{"日本語", "🎉", "café"}},
		{"\"line1\nline2\" third", []string{"line1\nline2", "third"}},
		{"unquoted\nnewline", []string{"unquoted", "newline"}},
		{"mixed \t  whitespace", []string{"mixed", "whitespace"}},
		{`"quoted \"text\""`, []string{`quoted \text\`}},
		{"trailing   ", []string{"trailing"}},
		{"   leading", []string{"leading"}},
	}
	for _, testCase := range cases {
		got := ParseCommandArgs(testCase.input)
		if len(got) == 0 && len(testCase.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, testCase.want) {
			t.Errorf("ParseCommandArgs(%q) = %#v, want %#v", testCase.input, got, testCase.want)
		}
	}
}

func TestExpandPromptTemplate(t *testing.T) {
	templates := []PromptTemplate{
		{Name: "explain", Content: "Explain $1 in ${2:-detail}"},
	}
	// A matching command expands with parsed arguments.
	if got := ExpandPromptTemplate("/explain src/main.go", templates); got != "Explain src/main.go in detail" {
		t.Fatalf("expanded = %q", got)
	}
	// Non-commands and unknown names are returned unchanged.
	if got := ExpandPromptTemplate("plain text", templates); got != "plain text" {
		t.Fatalf("plain = %q", got)
	}
	if got := ExpandPromptTemplate("/unknown arg", templates); got != "/unknown arg" {
		t.Fatalf("unknown = %q", got)
	}
	// Arguments may span newlines.
	templates = []PromptTemplate{{Name: "multi", Content: "args: $@"}}
	if got := ExpandPromptTemplate("/multi first\nsecond", templates); got != "args: first second" {
		t.Fatalf("newline args = %q", got)
	}
	// The command may be separated from its arguments by a newline.
	if got := ExpandPromptTemplate("/multi\nfirst", templates); got != "args: first" {
		t.Fatalf("newline command = %q", got)
	}
}

func TestLoadPromptTemplatesAndFiles(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	cwd := filepath.Join(root, "project")
	globalDir := filepath.Join(agentDir, "prompts")
	projectDir := filepath.Join(cwd, ConfigDirName, "prompts")
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	writeSettingsFile(t, filepath.Join(globalDir, "global.md"), "---\ndescription: Global template\n---\nGlobal content $1")
	writeSettingsFile(t, filepath.Join(projectDir, "project.md"), "Project first line\n\nmore")
	// Non-markdown files and directories are ignored.
	writeSettingsFile(t, filepath.Join(globalDir, "notes.txt"), "not a template")
	if err := os.MkdirAll(filepath.Join(globalDir, "nested.md"), 0o755); err != nil {
		t.Fatal(err)
	}

	templates := LoadPromptTemplates(LoadPromptTemplatesOptions{
		Cwd: cwd, AgentDir: agentDir, IncludeDefaults: true,
	})
	if len(templates) != 2 {
		t.Fatalf("templates = %+v", templates)
	}
	if templates[0].Name != "global" || templates[0].Description != "Global template" ||
		templates[0].Content != "Global content $1" || templates[0].SourceInfo.Scope != SourceScopeUser {
		t.Fatalf("global template = %+v", templates[0])
	}
	if templates[1].Name != "project" || templates[1].Description != "Project first line" ||
		templates[1].SourceInfo.Scope != SourceScopeProject {
		t.Fatalf("project template = %+v", templates[1])
	}
	// The project template's base dir is the prompts directory.
	if templates[1].SourceInfo.BaseDir != projectDir {
		t.Fatalf("baseDir = %s", templates[1].SourceInfo.BaseDir)
	}

	// Explicit paths load files and directories; missing paths are skipped.
	explicit := filepath.Join(root, "explicit")
	if err := os.MkdirAll(explicit, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSettingsFile(t, filepath.Join(explicit, "extra.md"), "Extra content")
	templates = LoadPromptTemplates(LoadPromptTemplatesOptions{
		Cwd: cwd, AgentDir: agentDir,
		PromptPaths: []string{explicit, filepath.Join(explicit, "extra.md"), filepath.Join(root, "missing")},
	})
	if len(templates) != 2 {
		t.Fatalf("templates = %+v", templates)
	}
	if templates[0].SourceInfo.Scope != SourceScopeTemporary || templates[0].SourceInfo.Origin != SourceOriginTopLevel {
		t.Fatalf("source info = %+v", templates[0].SourceInfo)
	}
	// Defaults are skipped when not requested.
	if templates := LoadPromptTemplates(LoadPromptTemplatesOptions{Cwd: cwd, AgentDir: agentDir}); len(templates) != 0 {
		t.Fatalf("templates = %+v", templates)
	}
}

func TestLoadTemplateFrontmatterArgumentHint(t *testing.T) {
	root := t.TempDir()

	required := filepath.Join(root, "required.md")
	writeSettingsFile(t, required, "---\ndescription: Deploy the app\nargument-hint: \"<environment> [--dry-run]\"\n---\nDeploy body")
	template := LoadTemplateFromFile(required, CreateSyntheticSourceInfo(required, "local", "", "", root))
	if template == nil {
		t.Fatal("template must load")
	}
	if template.Name != "required" || template.Description != "Deploy the app" ||
		!template.HasArgumentHint || template.ArgumentHint != "<environment> [--dry-run]" {
		t.Fatalf("template = %+v", template)
	}

	// An empty argument-hint stays unset.
	empty := filepath.Join(root, "empty.md")
	writeSettingsFile(t, empty, "---\nargument-hint: \"\"\n---\nbody")
	template = LoadTemplateFromFile(empty, CreateSyntheticSourceInfo(empty, "local", "", "", root))
	if template == nil || template.HasArgumentHint {
		t.Fatalf("template = %+v", template)
	}

	// A missing file yields nil.
	if template := LoadTemplateFromFile(filepath.Join(root, "missing.md"), SourceInfo{}); template != nil {
		t.Fatalf("template = %+v", template)
	}

	// A long first line is truncated to 60 characters with an ellipsis.
	long := filepath.Join(root, "long.md")
	writeSettingsFile(t, long, strings.Repeat("x", 80))
	template = LoadTemplateFromFile(long, CreateSyntheticSourceInfo(long, "local", "", "", root))
	if template == nil || len(template.Description) != 63 || !strings.HasSuffix(template.Description, "...") {
		t.Fatalf("description = %q", template.Description)
	}
}
