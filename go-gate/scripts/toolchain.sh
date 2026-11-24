# shellcheck shell=bash
# Sourced from the project root by gate.sh and by install.sh. When go.mod asks
# for a newer Go than the installed one, go switches to it for the project's
# commands: the gate's tools build with the same Go, so the builds the install
# makes are the ones each run finds in the build cache.
SELECTED=$(go env GOVERSION 2>/dev/null) || SELECTED=""
[ -z "$SELECTED" ] || [ "$SELECTED" = "$(GOTOOLCHAIN=local go env GOVERSION 2>/dev/null)" ] || export GOTOOLCHAIN="$SELECTED"
