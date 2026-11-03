package tmux_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/influento/tmux-plugins/internal/tmux"
	"github.com/influento/tmux-plugins/internal/tmux/tmuxtest"
)

func TestCommands(t *testing.T) {
	for _, c := range []struct {
		name string
		in   []string
		want [][]string
	}{
		{"none", nil, nil},
		{"one", []string{"a", "b"}, [][]string{{"a", "b"}}},
		{"trailing separator", []string{"a", ";"}, [][]string{{"a"}}},
		{"two, empty between", []string{"a", ";", ";", "b", "c", ";"}, [][]string{{"a"}, {"b", "c"}}},
		{"escaped semicolon is an argument", []string{"a", "b;", ";", "c"}, [][]string{{"a", "b;"}, {"c"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := tmux.Commands(c.in)
			if !slices.EqualFunc(got, c.want, slices.Equal) {
				t.Errorf("Commands(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// Batches keeps every command whole and in order, and each batch under
// the limits tmux enforces.
func TestBatches(t *testing.T) {
	var args []string
	for i := range 3000 {
		args = append(args, "set-option", "-g", fmt.Sprintf("@o%d", i), strings.Repeat("v", i%50), ";")
	}
	batches := tmux.Batches(args)
	if len(batches) < 2 {
		t.Fatalf("got %d batches, want several", len(batches))
	}
	var rejoined []string
	for _, b := range batches {
		size := 0
		for _, a := range b {
			size += len(a) + 1
		}
		if len(b) >= 1000 || size >= 16<<10 {
			t.Errorf("batch of %d args, %d bytes is over tmux's limit", len(b), size)
		}
		if b[len(b)-1] == ";" || b[0] == ";" {
			t.Errorf("batch starts or ends with a separator")
		}
		rejoined = append(rejoined, b...)
		rejoined = append(rejoined, ";")
	}
	if !slices.EqualFunc(tmux.Commands(rejoined), tmux.Commands(args), slices.Equal) {
		t.Error("the batches do not hold the same commands in the same order")
	}
}

// RunAll gets past tmux's command-line limit and returns every command's
// output in order.
func TestRunAll(t *testing.T) {
	srv := tmuxtest.Start(t, tmuxtest.Options{})
	var args []string
	for i := range 1500 {
		args = append(args, "display-message", "-p", fmt.Sprint(i), ";")
	}
	out, err := srv.Client.RunAll(context.Background(), args...)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 1500 || lines[0] != "0" || lines[1499] != "1499" {
		t.Errorf("got %d lines (%q … %q), want 0 … 1499", len(lines), lines[0], lines[len(lines)-1])
	}
}
