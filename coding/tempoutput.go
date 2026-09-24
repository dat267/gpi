package coding

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Full tool output that exceeds the truncation limits is written to a temp file so
// the model can read it back (the "[Output truncated. Full output: <path>]" line in
// the tool result). Upstream writes those files into tmpdir() and leaves them to
// the OS to age out.
//
// D161: the port sweeps its own instead. Where /tmp is tmpfs that scratch is RAM,
// and a single 174 MB command can sit there for days — measured on the author's
// host: 230 files / 442 MB after three days, none of it read again. Upstream can
// leave this to systemd-tmpfiles or a reboot; a port that is often run on tmpfs
// cannot, and the sweep is cheap enough to run exactly when growth happens.

// tempOutputBudgetBytes caps the scratch space this port's own temp output files
// may occupy. One oversized command still fits; the pile does not grow.
const tempOutputBudgetBytes int64 = 256 << 20

// tempOutputPrefixes are the file name prefixes the port writes full output
// under: the bash tool's executor, the shared output accumulator (used by the
// bash and PowerShell tools, and "pi-output" by default).
var tempOutputPrefixes = []string{"pi-bash-", "pi-output-", "pi-powershell-"}

// sweepTempOutputFiles deletes this port's own temp output files in dir, oldest
// first, until they occupy at most budget bytes. keep is the file that was just
// created and is never deleted; its own growth is not counted, because its final
// size is known only when the command ends.
//
// Best effort: a failure to remove scratch must never fail a tool call, so every
// error here is ignored. Returns how many files were removed and the bytes they
// held.
func sweepTempOutputFiles(dir, keep string, budget int64) (int, int64) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0
	}
	type scratchFile struct {
		path    string
		size    int64
		modTime time.Time
	}
	keepName := filepath.Base(keep)
	var files []scratchFile
	var total int64
	for _, entry := range entries {
		if !entry.Type().IsRegular() || entry.Name() == keepName || !isTempOutputName(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, scratchFile{
			path:    filepath.Join(dir, entry.Name()),
			size:    info.Size(),
			modTime: info.ModTime(),
		})
		total += info.Size()
	}
	if total <= budget {
		return 0, 0
	}
	sort.Slice(files, func(i, j int) bool { return files[i].modTime.Before(files[j].modTime) })
	var removed int
	var freed int64
	for _, file := range files {
		if total <= budget {
			break
		}
		if err := os.Remove(file.path); err != nil {
			continue
		}
		total -= file.size
		freed += file.size
		removed++
	}
	return removed, freed
}

// isTempOutputName reports whether a directory entry is one of this port's temp
// output files.
func isTempOutputName(name string) bool {
	if !strings.HasSuffix(name, ".log") {
		return false
	}
	for _, prefix := range tempOutputPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
