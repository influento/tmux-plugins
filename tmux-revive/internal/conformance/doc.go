// Package conformance holds the rules tmux-revive's attach must keep from
// the bash tmux-attach it replaces (dotfiles workstation/scripts): restore
// once per server, before any shell; never onto live work; one restore when
// terminals open together; land on the most recently used session.
//
// The bash driver runs tmux-attach from the dotfiles checkout named by
// TMUX_PLUGINS_DOTFILES, with a stub in place of tmux-resurrect's restore;
// the Go driver runs tmux-revive attach (P0 step 3).
//
// The package has no code outside its tests.
package conformance
