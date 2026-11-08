package ui

import (
	"encoding/json"
	"testing"
)

func TestFzfKey(t *testing.T) {
	cases := map[string]string{
		"C-Space": "ctrl-space", "C-b": "ctrl-b", "M-a": "alt-a", "f": "f", "F": "f",
		"Space": "space", "F1": "", "C-Up": "", "": "",
	}
	for in, want := range cases {
		if got := FzfKey(in); got != want {
			t.Errorf("FzfKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// The panel window is as wide as its columns are in the terminal beside
// it: 40 of 135 columns in 1440 px.
func TestPanelPixels(t *testing.T) {
	if got := panelPixels(40, 1440, 135); got != 435 {
		t.Errorf("panelPixels = %d, want 435", got)
	}
	if got := panelPixels(40, 0, 0); got != 408 {
		t.Errorf("panelPixels without a terminal = %d, want 408", got)
	}
}

// The toggle finds the client's panel by its title, tiled or floating, and
// the terminal by focus.
func TestSwayTreeFind(t *testing.T) {
	const tree = `{"id":1,"name":"root","nodes":[{"id":2,"name":"1","nodes":[
		{"id":3,"name":"term-a","focused":true,"rect":{"width":1010}}],
		"floating_nodes":[{"id":4,"name":"tmux-agent panel -dev-pts-3"}]}]}`
	var root swayNode
	if err := json.Unmarshal([]byte(tree), &root); err != nil {
		t.Fatal(err)
	}
	title := panelTitle("/dev/pts/3")
	if p := root.find(func(n *swayNode) bool { return n.Name == title }); p == nil || p.ID != 4 {
		t.Errorf("panel = %+v, want id 4", p)
	}
	if p := root.find(func(n *swayNode) bool { return n.Name == panelTitle("/dev/pts/9") }); p != nil {
		t.Errorf("another client's panel found: %+v", p)
	}
	if f := root.find(func(n *swayNode) bool { return n.Focused }); f == nil || f.Rect.Width != 1010 {
		t.Errorf("focused = %+v, want the terminal, 1010 px", f)
	}
}
