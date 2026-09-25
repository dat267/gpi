package interactive

import "regexp"

// ansiSequenceRe matches the SGR/OSC/CSI escapes test output carries. It lives
// in an untagged test file because the PTY harness that used to define it is
// !windows, while other cross-platform tests strip escapes too.
var ansiSequenceRe = regexp.MustCompile(`\x1b\[[0-9;?<>]*[a-zA-Z]|\x1b\][^\x07]*\x07|\x1b[>=]`)
