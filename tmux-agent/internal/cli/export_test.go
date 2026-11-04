package cli

import (
	"context"
	"io"
	"os"
)

// Pick runs the session picker with ctx as the command's context, so that
// a test can let the deadline pass while the picker is open.
func Pick(ctx context.Context) error {
	return newApp("test", io.Discard, io.Discard, os.Getenv).pick(ctx)
}
