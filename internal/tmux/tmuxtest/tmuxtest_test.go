package tmuxtest

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
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

// A stub whose sandbox is gone exits and leaves nothing behind: a test can
// end while a stub it started still runs. Such a stub spun forever trying
// to claim a call folder, or made the sandbox's folders again in /tmp.
func TestStubStopsWithoutSandbox(t *testing.T) {
	realMkdir, err := exec.LookPath("mkdir")
	if err != nil {
		t.Skip("no mkdir")
	}
	tests := []struct {
		name  string
		setup func(t *testing.T, sb *Sandbox)
	}{
		{"gone before the call", func(t *testing.T, sb *Sandbox) {
			if err := os.RemoveAll(sb.callsDir); err != nil {
				t.Fatal(err)
			}
		}},
		{"gone during the call", func(t *testing.T, sb *Sandbox) {
			// The calls folder goes right after each folder the stub makes.
			sb.Stub(t, "mkdir", "#!/bin/sh\n"+shellQuote(realMkdir)+" \"$@\" || exit\nrm -rf \"$TMUXTEST_CALLS\"\n")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sb := NewSandbox(t)
			tt.setup(t, sb)
			cmd := sb.Cmd(t, "notify-send", "a title")
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				_ = cmd.Process.Kill() // it may exit in the meantime
				<-done
				t.Fatal("the stub still ran 5 s after its sandbox went")
			}
			if _, err := os.Stat(sb.callsDir); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("the stub made its calls folder again (stat: %v)", err)
			}
		})
	}
}
