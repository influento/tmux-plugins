package tmuxtest

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// running reports whether process pid runs: it exists and is not a zombie
// waiting for its parent.
func running(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	f := strings.Fields(string(b[strings.LastIndexByte(string(b), ')')+1:]))
	return len(f) > 0 && f[0] != "Z"
}

// Nothing started in a sandbox outlives it: a program left behind, such
// as a notifier a hook starts in the background, could write into the
// sandbox while it is removed.
func TestSandboxStopsLeftovers(t *testing.T) {
	if _, err := os.Stat("/proc/self/environ"); err != nil {
		t.Skip("no /proc")
	}
	var pid int
	t.Run("sandbox", func(t *testing.T) {
		sb := NewSandbox(t)
		out, err := sb.Cmd(t, "sh", "-c", "sleep 60 >/dev/null 2>&1 & echo $!").Output()
		if err != nil {
			t.Fatal(err)
		}
		if pid, err = strconv.Atoi(strings.TrimSpace(string(out))); err != nil {
			t.Fatal(err)
		}
	})
	if pid == 0 {
		t.Fatal("no process started")
	}
	if running(pid) {
		t.Errorf("process %d outlived its sandbox", pid)
	}
}
