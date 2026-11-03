package shell

import "testing"

func TestQuote(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"nvim", "nvim"},
		{"/usr/bin/tmux-agent", "/usr/bin/tmux-agent"},
		{"--resume", "--resume"},
		{"", "''"},
		{"a file", "'a file'"},
		{"it's", `'it'\''s'`},
		{"$HOME;rm", "'$HOME;rm'"},
		{"=notes", "'=notes'"}, // zsh: the path of the command notes
		{"V=1", "'V=1'"},
		{"~/x", "'~/x'"},
		{"*.go", "'*.go'"},
	} {
		if got := Quote(c.in); got != c.want {
			t.Errorf("Quote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestJoin(t *testing.T) {
	if got, want := Join([]string{"ssh", "host-b", "it's"}), `ssh host-b 'it'\''s'`; got != want {
		t.Errorf("Join = %q, want %q", got, want)
	}
	if got := Join(nil); got != "" {
		t.Errorf("Join(nil) = %q, want empty", got)
	}
}
