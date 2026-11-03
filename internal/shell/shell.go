// Package shell quotes words for a POSIX shell or zsh.
package shell

import "strings"

// safe are the bytes a word may hold and still go unquoted. = is not one
// of them: zsh expands a word that starts with = to a command's path, and
// a first word with = in it is an assignment.
const safe = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_@%+:,./-"

// Quote quotes s for a POSIX shell or zsh, and leaves plain words alone.
func Quote(s string) string {
	if s != "" && strings.Trim(s, safe) == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Join quotes each word and joins them into one command line.
func Join(words []string) string {
	q := make([]string, len(words))
	for i, w := range words {
		q[i] = Quote(w)
	}
	return strings.Join(q, " ")
}
