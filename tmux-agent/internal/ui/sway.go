package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/influento/tmux-plugins/internal/shell"
)

// panelTitle is the sway window title of a client's panel: the toggle
// finds the window by it.
func panelTitle(client string) string {
	return "tmux-agent panel " + strings.NewReplacer("/", "-", `"`, "").Replace(client)
}

// SwayPanel reports whether the panel can open as a sway window beside the
// terminal: sway runs, and swaymsg and ghostty are there.
func SwayPanel(getenv func(string) string) bool {
	if getenv("SWAYSOCK") == "" {
		return false
	}
	for _, p := range []string{"swaymsg", "ghostty"} {
		if _, err := exec.LookPath(p); err != nil {
			return false
		}
	}
	return true
}

// swayNode is the part of a sway tree node the panel reads.
type swayNode struct {
	ID       int64               `json:"id"`
	Name     string              `json:"name"`
	Focused  bool                `json:"focused"`
	Rect     struct{ Width int } `json:"rect"`
	Nodes    []swayNode          `json:"nodes"`
	Floating []swayNode          `json:"floating_nodes"`
}

// find returns the first node in the tree that ok accepts.
func (n *swayNode) find(ok func(*swayNode) bool) *swayNode {
	if ok(n) {
		return n
	}
	for _, list := range [][]swayNode{n.Nodes, n.Floating} {
		for i := range list {
			if m := list[i].find(ok); m != nil {
				return m
			}
		}
	}
	return nil
}

func swayTree(ctx context.Context) (*swayNode, error) {
	out, err := exec.CommandContext(ctx, "swaymsg", "-t", "get_tree").Output()
	if err != nil {
		return nil, fmt.Errorf("swaymsg get_tree: %w", err)
	}
	var root swayNode
	if err := json.Unmarshal(out, &root); err != nil {
		return nil, fmt.Errorf("sway tree: %w", err)
	}
	return &root, nil
}

func swaymsg(ctx context.Context, command string) error {
	if out, err := exec.CommandContext(ctx, "swaymsg", command).CombinedOutput(); err != nil {
		return fmt.Errorf("swaymsg %s: %w: %s", command, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// panelPixels is how wide the panel window is for cols columns, at the
// cell width of the focused terminal: termPixels wide, clientCols columns.
// The padding covers the terminal's margins.
func panelPixels(cols, termPixels, clientCols int) int {
	const padding = 8
	if clientCols <= 0 || termPixels <= 0 {
		return cols*10 + padding
	}
	return (cols*termPixels+clientCols-1)/clientCols + padding
}

// ToggleSwayPanel closes the client's panel window when it is open, and
// otherwise opens one left of the focused terminal, cols wide, running
// command. clientCols is the client's width in columns.
func ToggleSwayPanel(ctx context.Context, command []string, client string, cols, clientCols int) error {
	title := panelTitle(client)
	isPanel := func(n *swayNode) bool { return n.Name == title }
	tree, err := swayTree(ctx)
	if err != nil {
		return err
	}
	if p := tree.find(isPanel); p != nil {
		return swaymsg(ctx, fmt.Sprintf("[con_id=%d] kill", p.ID))
	}
	width := panelPixels(cols, 0, clientCols)
	if term := tree.find(func(n *swayNode) bool { return n.Focused }); term != nil {
		width = panelPixels(cols, term.Rect.Width, clientCols)
	}
	line := shell.Join(append([]string{"ghostty", "--title=" + title, "-e"}, command...))
	if err := swaymsg(ctx, "exec "+line); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return errors.New("the panel window did not appear")
		case <-time.After(30 * time.Millisecond):
		}
		tree, err := swayTree(ctx)
		if err != nil {
			return err
		}
		if p := tree.find(isPanel); p != nil {
			return swaymsg(ctx, fmt.Sprintf("[con_id=%d] move left, resize set width %d px", p.ID, width))
		}
	}
}
