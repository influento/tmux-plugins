// Package applog appends lines to a plugin's log file, which moves to
// <name>.old past a size, so it never grows without end. Logging is best
// effort: a failure here never fails the caller.
package applog

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Append writes line, and a newline, to the file at path, after it moves
// a file past limit bytes to path.old. The folder is made 0700, the file
// 0600; a symlink in its place is refused. Two processes that move the
// file at once can lose the older half: the cost of keeping no lock.
func Append(path string, limit int64, line string) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	if fi, err := os.Lstat(path); err == nil && fi.Size() > limit {
		_ = os.Rename(path, path+".old") // best effort, as above
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(strings.TrimSuffix(line, "\n") + "\n") // one write: lines of processes never mix
}
