package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/influento/tmux-plugins/tmux-agent/internal/attention"
	"github.com/influento/tmux-plugins/tmux-agent/internal/store"
)

// exitNoTask is task end's exit code for a task that is not open: started
// before tmux-agent had tasks, or ended already. It closes nothing; the
// tool closes the windows by name.
const exitNoTask = 4

// taskJSON names a task in the JSON of start and task.
type taskJSON struct {
	Source string `json:"source"`
	ID     string `json:"id"`
	Title  string `json:"title"`
}

func taskOf(t store.Task) *taskJSON { return &taskJSON{Source: t.Source, ID: t.ID, Title: t.Title} }

// taskArgs are start's task flags.
type taskArgs struct{ id, source, title *string }

func addTaskFlags(fs *flag.FlagSet) taskArgs {
	return taskArgs{fs.String("task", "", ""), fs.String("source", "", ""), fs.String("title", "", "")}
}

// task is the task the flags name, or ok false for none. An error is a
// usage error.
func (t taskArgs) task() (tk store.Task, ok bool, err error) {
	if *t.id == "" {
		if *t.source != "" || *t.title != "" {
			return store.Task{}, false, errors.New("--source and --title go with --task")
		}
		return store.Task{}, false, nil
	}
	if !store.ValidName(*t.id) || !store.ValidName(*t.source) {
		return store.Task{}, false, errors.New("--task and --source take a name: letters, digits, '.', '_' and '-'")
	}
	if strings.ContainsFunc(*t.title, unicode.IsControl) {
		return store.Task{}, false, errors.New("--title holds a control character")
	}
	return store.Task{Source: *t.source, ID: *t.id, Title: *t.title}, true, nil
}

// openTask opens the task, or updates the title of an open one.
func (a *app) openTask(t store.Task) (store.Task, error) {
	cur, ok, err := a.env.Store.LoadTask(t.Source, t.ID)
	if err != nil {
		return store.Task{}, err
	}
	if ok && (t.Title == "" || t.Title == cur.Title) {
		return cur, nil
	}
	if ok {
		cur.Title = t.Title
	} else {
		cur = t
		cur.Started = time.Now().Unix()
	}
	if cur.Title == "" {
		cur.Title = cur.ID
	}
	return cur, a.env.Store.SaveTask(cur)
}

// boundPane is a pane bound to a task, and its window.
type boundPane struct {
	pane, task, agent, role, state, nestedRoles string
	win                                         window
}

// boundPanes lists the panes bound to a task, every task's for "".
func (a *app) boundPanes(ctx context.Context, key string) ([]boundPane, error) {
	format := strings.Join([]string{
		"#{pane_id}", "#{" + attention.OptTask + "}", "#{window_id}", "#{session_name}", "#{window_name}",
		"#{@agent}", "#{" + attention.OptRole + "}", "#{@attention}", "#{" + attention.OptNestedRoles + "}",
	}, "\x1f")
	out, err := a.tmux.Output(ctx, "list-panes", "-a", "-F", format)
	if err != nil {
		return nil, err
	}
	var bound []boundPane
	for _, l := range strings.Split(out, "\n") {
		f := strings.SplitN(l, "\x1f", 9)
		if len(f) < 9 || f[1] == "" || (key != "" && f[1] != key) {
			continue
		}
		bound = append(bound, boundPane{pane: f[0], task: f[1], win: window{f[2], f[3], f[4]},
			agent: f[5], role: f[6], state: f[7], nestedRoles: f[8]})
	}
	return bound, nil
}

// taskCommand runs `task end` and `task list`.
func (a *app) taskCommand(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(a.stderr, usage)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	var code int
	var err error
	switch args[0] {
	case "end":
		code, err = a.taskEnd(ctx, args[1:])
	case "list":
		err = a.taskList(ctx, args[1:])
	default:
		err = errUsage
	}
	switch {
	case errors.Is(err, errUsage):
		fmt.Fprint(a.stderr, usage)
		return 2
	case err != nil:
		fmt.Fprintf(a.stderr, "tmux-agent task %s: %v\n", args[0], err)
		return 1
	}
	return code
}

// endedJSON is what `task end --json` prints.
type endedJSON struct {
	Version int          `json:"version"`
	Task    *taskJSON    `json:"task"`
	Closed  []windowJSON `json:"closed"`
}

// taskEnd forgets a task, then with --close closes its windows, the
// caller's own last (merge and drop run in the task's window). An unknown
// task exits 4 and closes nothing.
func (a *app) taskEnd(ctx context.Context, args []string) (int, error) {
	fs := flag.NewFlagSet("task end", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	source := fs.String("source", "", "")
	closeWins := fs.Bool("close", false, "")
	asJSON := fs.Bool("json", false, "")
	id, rest := "", args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		id, rest = args[0], args[1:] // the id may come before the flags
	}
	if err := fs.Parse(rest); err != nil {
		return 0, errUsage
	}
	if id == "" && fs.NArg() == 1 {
		id = fs.Arg(0)
	} else if fs.NArg() > 0 {
		return 0, errUsage
	}
	if !store.ValidName(id) || !store.ValidName(*source) {
		return 0, errUsage
	}
	t, ok, err := a.env.Store.LoadTask(*source, id)
	if err != nil {
		return 0, err
	}
	if !ok {
		fmt.Fprintf(a.stderr, "tmux-agent task end: no open task %s/%s\n", *source, id)
		return exitNoTask, nil
	}
	bound, err := a.boundPanes(ctx, t.Key())
	if err != nil {
		return 0, err
	}
	if _, err := a.env.Store.DeleteTask(t.Source, t.ID); err != nil {
		return 0, err
	}
	var panes []string
	var wins []window
	for _, b := range bound {
		panes = append(panes, b.pane)
		if !slices.Contains(wins, b.win) {
			wins = append(wins, b.win)
		}
	}
	if err := a.env.UnbindTask(ctx, t.Key(), panes); err != nil {
		return 0, err
	}
	print := func(closed []window) error {
		if *asJSON {
			out := endedJSON{Version: contractVersion, Task: taskOf(t), Closed: []windowJSON{}}
			for _, w := range closed {
				out.Closed = append(out.Closed, windowJSON{w.id, w.session, w.name})
			}
			return json.NewEncoder(a.stdout).Encode(out)
		}
		return a.printClosed(closed, false)
	}
	if !*closeWins {
		return 0, print(nil)
	}
	return 0, a.closeThese(ctx, wins, print)
}

// listJSON is what `task list --json` prints.
type listJSON struct {
	Version int            `json:"version"`
	Tasks   []taskListJSON `json:"tasks"`
}

type taskListJSON struct {
	taskJSON
	Started int64          `json:"started,omitempty"`
	Panes   []taskPaneJSON `json:"panes"`
}

// taskPaneJSON is a pane bound to a task: its window, its agent, the role
// it runs as, its flag, and the roles of the runs below it.
type taskPaneJSON struct {
	Pane    string   `json:"pane"`
	Window  string   `json:"window"`
	Session string   `json:"session"`
	Name    string   `json:"name"`
	Agent   string   `json:"agent,omitempty"`
	Role    string   `json:"role,omitempty"`
	State   string   `json:"state,omitempty"`
	Runs    []string `json:"runs,omitempty"`
}

// taskList prints the open tasks, their windows and roles.
func (a *app) taskList(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("task list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	asJSON := fs.Bool("json", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return errUsage
	}
	tasks, err := a.env.Store.Tasks()
	if err != nil {
		return err
	}
	bound, err := a.boundPanes(ctx, "")
	if err != nil {
		return err
	}
	out := listJSON{Version: contractVersion, Tasks: []taskListJSON{}}
	for _, t := range tasks {
		tl := taskListJSON{taskJSON: *taskOf(t), Started: t.Started, Panes: []taskPaneJSON{}}
		for _, b := range bound {
			if b.task != t.Key() {
				continue
			}
			var runs []string
			for _, r := range strings.Fields(b.nestedRoles) {
				role, _, _ := strings.Cut(r, "@")
				runs = append(runs, role)
			}
			tl.Panes = append(tl.Panes, taskPaneJSON{Pane: b.pane, Window: b.win.id, Session: b.win.session,
				Name: b.win.name, Agent: b.agent, Role: b.role, State: b.state, Runs: runs})
		}
		out.Tasks = append(out.Tasks, tl)
	}
	if *asJSON {
		return json.NewEncoder(a.stdout).Encode(out)
	}
	for _, t := range out.Tasks {
		var parts []string
		for _, p := range t.Panes {
			s := p.Session + ":" + p.Name
			if p.Role != "" {
				s += " " + p.Role
			}
			if len(p.Runs) > 0 {
				s += " (" + strings.Join(p.Runs, " ") + ")"
			}
			if p.State != "" {
				s += " " + p.State
			}
			parts = append(parts, s)
		}
		if _, err := fmt.Fprintf(a.stdout, "%s/%s\t%s\t%s\n", t.Source, t.ID, t.Title, strings.Join(parts, ", ")); err != nil {
			return err
		}
	}
	return nil
}
