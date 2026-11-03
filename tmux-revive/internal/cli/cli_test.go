package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParseWhen(t *testing.T) {
	loc := time.FixedZone("test", 3600)
	now := time.Date(2026, 11, 3, 18, 30, 0, 0, loc)
	for _, c := range []struct {
		in   string
		want time.Time
	}{
		{"14:02:37", time.Date(2026, 11, 3, 14, 2, 37, 0, loc)},
		{"14:02", time.Date(2026, 11, 3, 14, 2, 0, 0, loc)},
		{"2026-11-01 09:05", time.Date(2026, 11, 1, 9, 5, 0, 0, loc)},
		{"2026-11-01 09:05:07", time.Date(2026, 11, 1, 9, 5, 7, 0, loc)},
		{"2026-11-01T09:05:07", time.Date(2026, 11, 1, 9, 5, 7, 0, loc)},
		{"2026-11-01T09:05:07Z", time.Date(2026, 11, 1, 9, 5, 7, 0, time.UTC)},
	} {
		got, err := parseWhen(c.in, now)
		if err != nil || !got.Equal(c.want) {
			t.Errorf("parseWhen(%q) = %v, %v; want %v", c.in, got, err, c.want)
		}
	}
	if _, err := parseWhen("yesterday", now); err == nil {
		t.Error("parseWhen(yesterday) did not fail")
	}
}

func TestUsageAndVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Main("v1.2.3", []string{"--version"}, &out, &errOut); code != 0 || out.String() != "v1.2.3\n" {
		t.Errorf("--version: code %d, %q", code, out.String())
	}
	for _, args := range [][]string{nil, {"nope"}, {"forget-session"}, {"shell-init"}, {"save", "extra"}} {
		out.Reset()
		errOut.Reset()
		if code := Main("dev", args, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "usage:") {
			t.Errorf("%q: code %d, stderr %q; want 2 and the usage", args, code, errOut.String())
		}
	}
}

// The zsh hooks are valid zsh, and quote the program names they hold.
func TestShellInitZsh(t *testing.T) {
	a := newApp("dev", nil, nil, func(string) string { return "" })
	var out bytes.Buffer
	a.stdout = &out
	if err := a.shellInit("zsh"); err != nil {
		t.Fatal(err)
	}
	script := out.String()
	for _, want := range []string{"add-zsh-hook chpwd", "ZSH_SUBSHELL", "'nvim'", "'ssh'", "&!"} {
		if !strings.Contains(script, want) {
			t.Errorf("script lacks %q:\n%s", want, script)
		}
	}
	if err := a.shellInit("bash"); err == nil {
		t.Error("shell-init bash did not fail")
	}
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh not installed")
	}
	f := filepath.Join(t.TempDir(), "init.zsh")
	if err := os.WriteFile(f, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, err := exec.Command(zsh, "-n", f).CombinedOutput(); err != nil {
		t.Errorf("zsh -n: %v\n%s", err, b)
	}
}

func TestHookEvents(t *testing.T) {
	ev := TmuxHookEvents()
	if !slices.Contains(ev, "after-set-option") || !slices.Contains(ev, "session-closed") || len(ev) != len(saveHooks)+1 {
		t.Errorf("events = %v", ev)
	}
	if !strings.Contains(optionHook, "#{hook_argument_0}") || !strings.Contains(optionHook, "@revive_cmd") {
		t.Errorf("optionHook = %q", optionHook)
	}
}
