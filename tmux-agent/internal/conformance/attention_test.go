package conformance

// Flags, counts, the list, the pickers' rows, jump and borders: what
// tmux-attention does today (dotfiles common/scripts/CLAUDE.md, "The
// attention model").

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

// set prints the previous state only when the state changes.
func TestSetPrintsPreviousStateOnChange(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		p := e.firstPane(t)
		if got := e.d.Set(t, e.Server, p, Flag{State: "working", Quiet: true}); got != "idle" {
			t.Errorf("first set printed %q, want idle", got)
		}
		if got := e.d.Set(t, e.Server, p, Flag{State: "working", Reason: "build", Quiet: true}); got != "" {
			t.Errorf("same state printed %q, want nothing", got)
		}
		e.wantFlag(t, p, "working", "build") // the reason still follows
		if got := e.d.Set(t, e.Server, p, Flag{State: "needs-you", Reason: "Bash(ls)", Quiet: true}); got != "working" {
			t.Errorf("change printed %q, want working", got)
		}
		e.wantFlag(t, p, "needs-you", "Bash(ls)")
	})
}

func TestSetRecordsTimeAndOwner(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		p := e.firstPane(t)
		before := time.Now().Unix()
		e.d.Set(t, e.Server, p, Flag{State: "done", Owner: "sh", Quiet: true})
		at, err := strconv.ParseInt(e.opt(t, "-p", p, "@attention_at"), 10, 64)
		if err != nil || at < before-1 || at > time.Now().Unix()+1 {
			t.Errorf("@attention_at = %d (%v), want about now", at, err)
		}
		if got := e.opt(t, "-p", p, "@attention_owner"); got != "sh" {
			t.Errorf("@attention_owner = %q, want sh", got)
		}
	})
}

// A window's flag is its highest-ranked pane's: needs-you, done, working.
func TestWindowRollUp(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		p1 := e.firstPane(t)
		p2 := e.split(t, p1, "")
		p3 := e.split(t, p1, "")
		win := func() string { return e.opt(t, "-w", p1, "@attention_win") }
		steps := []struct {
			do   func()
			want string
		}{
			{func() { e.d.Set(t, e.Server, p1, Flag{State: "working", Quiet: true}) }, "working"},
			{func() { e.d.Set(t, e.Server, p2, Flag{State: "done", Quiet: true}) }, "done"},
			{func() { e.d.Set(t, e.Server, p3, Flag{State: "needs-you", Quiet: true}) }, "needs-you"},
			{func() { e.d.Clear(t, e.Server, p3) }, "done"},
			{func() { e.d.Clear(t, e.Server, p2) }, "working"},
			{func() { e.d.Clear(t, e.Server, p1) }, ""},
		}
		for i, s := range steps {
			s.do()
			if got := win(); got != s.want {
				t.Errorf("step %d: @attention_win = %q, want %q", i, got, s.want)
			}
		}
	})
}

// The counts in the status bar follow every change, in the theme colours:
// needs you, working, done, then a separator.
func TestStatusCounts(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		status := func() string { return e.opt(t, "-g", "", "@attention_status") }
		p := e.firstPane(t)
		ps := []string{p, e.split(t, p, ""), e.newWindow(t, "proj-a", "", ""), e.newWindow(t, "proj-a", "", "")}
		e.d.Set(t, e.Server, ps[0], Flag{State: "needs-you", Quiet: true})
		e.d.Set(t, e.Server, ps[1], Flag{State: "needs-you", Quiet: true})
		e.d.Set(t, e.Server, ps[2], Flag{State: "working", Quiet: true})
		e.d.Set(t, e.Server, ps[3], Flag{State: "done", Quiet: true})
		want := "#[fg=#ff0001]? 2  #[fg=#0000f3]● 1  #[fg=#00ff02]✓ 1  #[fg=#444444]│"
		if got := status(); got != want {
			t.Errorf("@attention_status = %q, want %q", got, want)
		}
		for _, p := range ps {
			e.d.Clear(t, e.Server, p)
		}
		if got := status(); got != "" {
			t.Errorf("@attention_status with no flags = %q, want empty", got)
		}
	})
}

// Without theme options the counts fall back to plain colour names.
func TestStatusDefaultColours(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		for _, o := range []string{"needs_you", "done", "working", "dim"} {
			e.MustTmux(t, "set-option", "-gu", "@attention_fg_"+o)
		}
		e.d.Set(t, e.Server, e.firstPane(t), Flag{State: "needs-you", Quiet: true})
		if got, want := e.opt(t, "-g", "", "@attention_status"), "#[fg=red]? 1  #[fg=default]│"; got != want {
			t.Errorf("@attention_status = %q, want %q", got, want)
		}
	})
}

func TestClearIsIdempotent(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		p := e.firstPane(t)
		e.d.Clear(t, e.Server, p)
		e.d.Set(t, e.Server, p, Flag{State: "done", Reason: "x", Quiet: true})
		e.d.Clear(t, e.Server, p)
		e.wantFlag(t, p, "", "")
		for _, o := range []string{"@attention_at", "@attention_owner", "@attention_gone"} {
			if got := e.opt(t, "-p", p, o); got != "" {
				t.Errorf("%s = %q after clear", o, got)
			}
		}
	})
}

// Focus marks a done pane seen; other states stay.
func TestSeenClearsOnlyDone(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		for _, state := range []string{"done", "working", "needs-you"} {
			p := e.newWindow(t, "proj-a", "", "")
			e.d.Set(t, e.Server, p, Flag{State: state, Quiet: true})
			e.d.Seen(t, e.Server, p)
			want := state
			if state == "done" {
				want = ""
			}
			if got, _ := e.flag(t, p); got != want {
				t.Errorf("seen on %s: flag = %q, want %q", state, got, want)
			}
		}
	})
}

// A flag set with an owner lives only while the owner is the pane's
// command; a sweep drops it and leaves a note. A flag without an owner is
// never swept.
func TestSweepDropsFlagsOfDeadOwners(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		shell := e.firstPane(t)
		live := e.claudeWindow(t, "proj-a")
		unowned := e.newWindow(t, "proj-a", "", "")
		e.d.Set(t, e.Server, shell, Flag{State: "working", Owner: "claude", Quiet: true})
		e.d.Set(t, e.Server, live, Flag{State: "working", Owner: "claude", Quiet: true})
		e.d.Set(t, e.Server, unowned, Flag{State: "done", Quiet: true})
		e.d.Sweep(t, e.Server)

		e.wantFlag(t, shell, "", "")
		if got := e.opt(t, "-p", shell, "@attention_gone"); got != "working · claude exited" {
			t.Errorf("@attention_gone = %q, want %q", got, "working · claude exited")
		}
		if got := e.opt(t, "-w", shell, "@attention_win"); got != "" {
			t.Errorf("window flag after the sweep = %q, want none", got)
		}
		e.wantFlag(t, live, "working", "")
		e.wantFlag(t, unowned, "done", "")
	})
}

// Any count, list or focus sweeps first, so a dead flag never shows.
func TestSweepRunsBeforeStatusListAndSeen(t *testing.T) {
	triggers := map[string]func(e *env, t *testing.T, other string){
		"status": func(e *env, t *testing.T, _ string) { e.d.Status(t, e.Server) },
		"list":   func(e *env, t *testing.T, _ string) { e.d.List(t, e.Server) },
		"seen":   func(e *env, t *testing.T, other string) { e.d.Seen(t, e.Server, other) },
	}
	for name, trigger := range triggers {
		t.Run(name, func(t *testing.T) {
			each(t, func(t *testing.T, e *env) {
				dead := e.firstPane(t)
				other := e.newWindow(t, "proj-a", "", "")
				e.d.Set(t, e.Server, dead, Flag{State: "needs-you", Owner: "claude", Quiet: true})
				trigger(e, t, other)
				e.wantFlag(t, dead, "", "")
				if got := e.opt(t, "-g", "", "@attention_status"); got != "" {
					t.Errorf("@attention_status = %q, want empty", got)
				}
			})
		})
	}
}

// The list: flagged panes by rank, oldest first; then idle agents and
// dropped flags; then everything else. Hidden overlay sessions are left out.
func TestListOrderAndDetails(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		shell := e.firstPane(t)
		needsOld := e.newWindow(t, "proj-a", "w-needs-old", "")
		needsNew := e.newWindow(t, "proj-a", "w-needs-new", "")
		done := e.newWindow(t, "proj-a", "w-done", "")
		working := e.newWindow(t, "proj-a", "w-working", "")
		idle := e.claudeWindow(t, "proj-a")
		e.MustTmux(t, "select-pane", "-t", idle, "-T", "✳ fix the tests")
		gone := e.newWindow(t, "proj-a", "w-gone", "")
		e.newSession(t, "ov-nvim-proj-a", "")

		e.d.Set(t, e.Server, needsNew, Flag{State: "needs-you", Reason: "Bash(make)", Quiet: true})
		e.d.Set(t, e.Server, needsOld, Flag{State: "needs-you", Quiet: true})
		e.d.Set(t, e.Server, done, Flag{State: "done", Reason: "turn ended", Quiet: true})
		e.d.Set(t, e.Server, working, Flag{State: "working", Quiet: true})
		e.d.Set(t, e.Server, gone, Flag{State: "working", Owner: "claude", Quiet: true})
		e.setAge(t, needsOld, 2*time.Hour)
		e.setAge(t, needsNew, 90*time.Second)
		e.setAge(t, done, 3*24*time.Hour)
		// Ages are in minutes or more: the list reads the clock after
		// setAge, and an age in seconds would change with the wait.
		e.setAge(t, working, 10*time.Minute)

		rows := e.d.List(t, e.Server)
		var got []string
		for _, r := range rows {
			if strings.HasPrefix(r.Target, "ov-") {
				t.Errorf("hidden session listed: %+v", r)
			}
			got = append(got, fmt.Sprintf("%d %s %s %s %s", r.Rank, r.Pane, r.Glyph, r.Detail, r.Age))
		}
		want := []string{
			fmt.Sprintf("0 %s ? needs-you 2h", needsOld),
			fmt.Sprintf("0 %s ? Bash(make) 1m", needsNew),
			fmt.Sprintf("1 %s ✓ turn ended 3d", done),
			fmt.Sprintf("2 %s ● working 10m", working),
			fmt.Sprintf("3 %s   idle · fix the tests -", idle),
			fmt.Sprintf("3 %s   flag dropped · working · claude exited -", gone),
			fmt.Sprintf("4 %s   sh -", shell),
		}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("list:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
		for _, r := range rows {
			if r.Pane == needsOld && (r.Window != "w-needs-old" || r.Target != "proj-a:1.0") {
				t.Errorf("row %+v: want window w-needs-old at proj-a:1.0", r)
			}
		}
	})
}

// The session picker: one row per session with its windows' flags inline,
// flagged sessions first, overlay sessions left out, names cut to 16.
func TestSessionRows(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		e.MustTmux(t, "rename-window", "-t", "proj-a:0", "alpha")
		e.newWindow(t, "proj-a", "abcdefghijklmnopqrstuvwxyz", "")
		e.newSession(t, "proj-b", "")
		e.MustTmux(t, "rename-window", "-t", "proj-b:0", "beta")
		flagged := e.newWindow(t, "proj-b", "gamma", "")
		e.newSession(t, "ov-nvim-proj-b", "")
		e.d.Set(t, e.Server, flagged, Flag{State: "needs-you", Quiet: true})

		rows := e.d.SessionRows(t, e.Server)
		if len(rows) != 2 || rows[0].Session != "proj-b" || rows[1].Session != "proj-a" {
			t.Fatalf("rows = %+v, want proj-b (flagged) then proj-a, no overlay", rows)
		}
		if want := fmt.Sprintf("%-14.14s ", "proj-b"); !strings.HasPrefix(rows[0].Display, want) {
			t.Errorf("row %q does not start with the session name padded to 14", rows[0].Display)
		}
		if !strings.Contains(rows[0].Display, "? gamma") || !strings.Contains(rows[0].Display, "  beta") {
			t.Errorf("proj-b row %q lacks its windows with glyph slots", rows[0].Display)
		}
		if !strings.Contains(rows[1].Display, "abcdefghijklmnop") || strings.Contains(rows[1].Display, "abcdefghijklmnopq") {
			t.Errorf("proj-a row %q: window name not cut at 16", rows[1].Display)
		}
		// Window columns line up across rows.
		if i, j := strings.Index(rows[0].Display, "gamma"), strings.Index(rows[1].Display, "abcdefghijklmnop"); i != j {
			t.Errorf("second column at %d and %d, want aligned:\n%s\n%s", i, j, rows[0].Display, rows[1].Display)
		}
	})
}

// The picker refuses to kill the last session: the terminal would close.
func TestKillSessionRefusesTheLast(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		e.newSession(t, "proj-b", "")
		e.newSession(t, "ov-nvim-proj-a", "") // hidden sessions do not count
		e.Attach(t, "proj-a", 100, 30)
		e.d.KillSession(t, e.Server, "proj-b")
		if _, err := e.Tmux("has-session", "-t", "=proj-b"); err == nil {
			t.Error("proj-b survived kill-session")
		}
		e.d.KillSession(t, e.Server, "proj-a")
		if _, err := e.Tmux("has-session", "-t", "=proj-a"); err != nil {
			t.Error("the last session was killed")
		}
	})
}

// prefix o: the next pane that needs you, else the next unread done; a
// second press walks on. Landing on a done pane marks it seen.
func TestJumpWalksFlaggedPanes(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		start := e.firstPane(t)
		n1 := e.newWindow(t, "proj-a", "w-n1", "")
		e.newSession(t, "proj-b", "")
		n2 := e.newWindow(t, "proj-b", "w-n2", "")
		d1 := e.newWindow(t, "proj-a", "w-d1", "")
		e.d.Set(t, e.Server, n1, Flag{State: "needs-you", Quiet: true})
		e.d.Set(t, e.Server, n2, Flag{State: "needs-you", Quiet: true})
		e.d.Set(t, e.Server, d1, Flag{State: "done", Quiet: true})
		e.setAge(t, n1, time.Minute)
		e.setAge(t, n2, 30*time.Second)
		e.Attach(t, "proj-a", 100, 30)
		current := func() string { return e.MustTmux(t, "display-message", "-p", "-c", e.clientName(t), "#{pane_id}") }

		for i, step := range []struct{ from, want string }{
			{start, n1},
			{n1, n2},
			{n2, d1},
			{d1, n1}, // d1 was seen on arrival, so it left the walk
		} {
			e.d.Jump(t, e.Server, step.from)
			tmuxPaneWait(t, e, current, step.want, i)
		}
		if got, _ := e.flag(t, d1); got != "" {
			t.Errorf("done pane still flagged %q after the jump landed on it", got)
		}
	})
}

func tmuxPaneWait(t *testing.T, e *env, current func() string, want string, step int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		if got = current(); got == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("jump %d: client on pane %s, want %s", step, got, want)
}

func (e *env) clientName(t *testing.T) string {
	t.Helper()
	out := e.MustTmux(t, "list-clients", "-F", "#{client_name}")
	name, _, _ := strings.Cut(out, "\n")
	return name
}

// Pane borders show only while a window is split.
func TestBordersFollowSplits(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		p := e.firstPane(t)
		win := e.Display(t, p, "#{window_id}")
		border := func() string { return e.opt(t, "-w", win, "pane-border-status") }
		q := e.split(t, p, "")
		e.d.Borders(t, e.Server, win)
		if got := border(); got != "top" {
			t.Errorf("split window: pane-border-status = %q, want top", got)
		}
		e.MustTmux(t, "kill-pane", "-t", q)
		e.d.Borders(t, e.Server, win)
		if got := border(); got != "off" {
			t.Errorf("single pane: pane-border-status = %q, want off", got)
		}
	})
}
