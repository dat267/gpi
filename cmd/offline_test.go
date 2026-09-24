package cmd

import (
	"os"
	"testing"

	"github.com/dat267/pier/coding"
)

// Offline is process-wide upstream: main.ts sets PI_OFFLINE and
// PI_SKIP_VERSION_CHECK from the flag or the environment before anything reads
// either, and the model runtime and the version check both consult the
// environment rather than the parsed arguments.
func TestApplyOfflineModeSetsTheProcessWideFlags(t *testing.T) {
	cases := []struct {
		name        string
		flag        bool
		env         string
		wantOffline bool
	}{
		{"flag", true, "", true},
		{"env 1", false, "1", true},
		{"env true", false, "true", true},
		{"env yes", false, "yes", true},
		{"env 0", false, "0", false},
		{"env false", false, "false", false},
		{"online", false, "", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("PI_OFFLINE", testCase.env)
			t.Setenv("PI_SKIP_VERSION_CHECK", "")
			applyOfflineMode(&coding.Args{Offline: testCase.flag})
			// The env is not cleared when it is not offline: upstream leaves a
			// falsy flag alone, it only ever sets the two variables.
			if got := coding.IsTruthyEnvFlag(os.Getenv("PI_OFFLINE")); got != testCase.wantOffline {
				t.Errorf("PI_OFFLINE = %q, want offline=%v", os.Getenv("PI_OFFLINE"), testCase.wantOffline)
			}
			if got := os.Getenv("PI_SKIP_VERSION_CHECK") != ""; got != testCase.wantOffline {
				t.Errorf("PI_SKIP_VERSION_CHECK = %q, want set=%v", os.Getenv("PI_SKIP_VERSION_CHECK"), testCase.wantOffline)
			}
		})
	}
}
