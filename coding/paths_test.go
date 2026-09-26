package coding

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestIsLocalPath(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"./skills", true},
		{"/abs/skills", true},
		{"~/skills", true},
		{"skills", true},
		// file: URLs are local, as upstream resolves them.
		{"file:///tmp/skills", true},
		{"npm:some-package", false},
		{"git:github.com/x/y", false},
		{"github:x/y", false},
		{"https://example.com/skills.tgz", false},
		{"http://example.com/skills.tgz", false},
		{"ssh:host/path", false},
		{"  npm:padded  ", false},
	}
	for _, tc := range cases {
		if got := IsLocalPath(tc.in); got != tc.want {
			t.Errorf("IsLocalPath(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestResolveCLIPaths(t *testing.T) {
	cwd := t.TempDir()

	if got := ResolveCLIPaths(cwd, nil); got != nil {
		t.Errorf("ResolveCLIPaths(nil) = %#v, want nil", got)
	}

	// Use a path that is absolute on this platform. "/abs/skills" is neither
	// absolute nor rooted-with-a-drive on Windows, and Node's path.isAbsolute
	// agrees, so the port resolves it against cwd exactly as upstream would.
	// ("\abs\skills" would not do either: Windows calls that rooted, not
	// absolute.)
	absolute := "/abs/skills"
	if runtime.GOOS == "windows" {
		absolute = filepath.VolumeName(cwd) + `\abs\skills`
	}

	got := ResolveCLIPaths(cwd, []string{"./skills", "npm:pkg", absolute})
	want := []string{filepath.Join(cwd, "skills"), "npm:pkg", absolute}
	if len(got) != len(want) {
		t.Fatalf("ResolveCLIPaths = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ResolveCLIPaths[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
