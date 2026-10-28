package main

import "testing"

// A server that is gone is an error, not a prompt the user has yet to
// answer: warp must not wait out the input timeout.
func TestReadOptionValueReportsDeadServer(t *testing.T) {
	t.Setenv("TMUX", t.TempDir()+"/gone,1,0")
	t.Setenv("TMUX_PANE", "%0")
	if v, ok, err := readOptionValue(optQuery); err == nil {
		t.Fatalf("readOptionValue on a dead server = %q, %v, nil; want an error", v, ok)
	}
}
