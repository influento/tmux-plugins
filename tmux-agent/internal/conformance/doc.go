// Package conformance holds the behaviour tmux-agent must keep from the bash
// scripts it replaces (tmux-attention and claude-tmux in dotfiles).
//
// Each case runs on a private tmux server through a Driver, one per
// implementation. The bash driver runs the scripts from the dotfiles checkout
// named by TMUX_PLUGINS_DOTFILES; the Go driver runs tmux-agent (the test
// binary itself, through package selftest).
// Commands, the title option and the record format may differ between the
// two; the drivers translate, and the cases check only what a user sees:
// flags, counts, names, notifications, and whether a conversation would
// resume.
//
// The cases read and write the attention options directly, so every
// implementation keeps them: @attention, @attention_reason, @attention_at,
// @attention_owner and @attention_gone on panes, @attention_win on windows,
// the global @attention_status, and the theme colours @attention_fg_*.
//
// The package has no code outside its tests.
package conformance
