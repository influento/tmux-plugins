package tmux

import (
	"context"
	"slices"
	"strings"
)

// tmux refuses a command line over about 16 KiB or 1,000 arguments
// ("command too long"); Batches stays well under both.
const (
	batchBytes = 8 << 10
	batchArgs  = 400
)

// Commands splits args at each lone ";", as tmux does. Empty commands are
// dropped.
func Commands(args []string) [][]string {
	var cmds [][]string
	for len(args) > 0 {
		i := slices.Index(args, ";")
		if i < 0 {
			i = len(args)
		}
		if i > 0 {
			cmds = append(cmds, args[:i])
		}
		args = args[min(i+1, len(args)):]
	}
	return cmds
}

// Batches packs the commands in args (separated by lone ";") into as few
// command lines as tmux accepts. A command is never split.
func Batches(args []string) [][]string {
	var batches [][]string
	var batch []string
	size := 0
	for _, cmd := range Commands(args) {
		n := len(cmd)
		for _, a := range cmd {
			n += len(a)
		}
		if len(batch) > 0 && (size+n > batchBytes || len(batch)+len(cmd) >= batchArgs) {
			batches = append(batches, batch)
			batch, size = nil, 0
		}
		if len(batch) > 0 {
			batch = append(batch, ";")
		}
		batch = append(batch, cmd...)
		size += n + 2
	}
	if len(batch) > 0 {
		batches = append(batches, batch)
	}
	return batches
}

// RunAll runs the commands in args in as few tmux calls as tmux accepts,
// and returns their stdout joined in order. It stops at the first call that
// fails; tmux itself stops a command line at its first failing command.
func (c *Client) RunAll(ctx context.Context, args ...string) (string, error) {
	var out strings.Builder
	for _, b := range Batches(args) {
		s, err := c.Run(ctx, b...)
		out.WriteString(s)
		if err != nil {
			return out.String(), err
		}
	}
	return out.String(), nil
}
