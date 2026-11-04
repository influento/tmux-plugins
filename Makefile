# Build, test, link for development, and release the three plugins.

SHELL := bash
.SHELLFLAGS := -euo pipefail -c
.DEFAULT_GOAL := build

PLUGINS := tmux-warp tmux-agent tmux-revive
ARCHES := amd64 arm64
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GOFLAGS_BUILD := -trimpath -ldflags "-s -w -X main.version=$(VERSION)"
PREFIX ?= $(HOME)/.local/bin
# The dotfiles checkout whose bash scripts the characterization tests run,
# taken at the commit CI pins: dotfiles deletes the scripts at the cutover.
DOTFILES ?= $(abspath $(CURDIR)/../dotfiles)
DOTFILES_REF := $(shell sed -n 's/^[[:space:]]*DOTFILES_REF:[[:space:]]*//p' .github/workflows/ci.yml)

.PHONY: build test vet dist dev undev release clean

## build: the three binaries for this machine, in bin/
build:
	@mkdir -p bin
	@for p in $(PLUGINS); do CGO_ENABLED=0 go build $(GOFLAGS_BUILD) -o "bin/$$p" "./$$p"; done

## vet: go vet, a gofmt check (dot folders such as CI's .dotfiles checkout
## are not ours), and actions pinned by commit SHA (a tag can be moved)
vet:
	go vet ./...
	@out=$$(find . -path './.*' -prune -o -name '*.go' -print | xargs -r gofmt -l); \
	  [[ -z "$$out" ]] || { echo "gofmt needed:"; echo "$$out"; exit 1; }
	@bad=$$(grep -nE '^[[:space:]-]*uses:' .github/workflows/*.yml | grep -vE '@[0-9a-f]{40}([[:space:]]|$$)' || true); \
	  [[ -z "$$bad" ]] || { echo "actions not pinned to a commit SHA:"; echo "$$bad"; exit 1; }

## test: vet, then every test with the race detector; the characterization
## tests run the scripts of $(DOTFILES) at $(DOTFILES_REF), extracted to a
## temporary folder
test: vet
	@if git -C "$(DOTFILES)" cat-file -e "$(DOTFILES_REF)^{commit}" 2>/dev/null; then \
	  scripts=$$(mktemp -d); trap 'rm -rf "$$scripts"' EXIT; \
	  git -C "$(DOTFILES)" archive "$(DOTFILES_REF)" common/scripts workstation/scripts | tar -x -C "$$scripts"; \
	  export TMUX_PLUGINS_DOTFILES="$$scripts"; \
	elif [[ -d "$(DOTFILES)" ]]; then \
	  echo "$(DOTFILES) has no commit $(DOTFILES_REF), so the bash scripts are not tested: git -C $(DOTFILES) fetch" >&2; \
	fi; \
	go test -race ./...

## dist: release assets in dist/, all plugins for linux amd64 and arm64, the
## warp wrapper, and SHA256SUMS over all of them
dist:
	rm -rf dist && mkdir -p dist
	@for a in $(ARCHES); do for p in $(PLUGINS); do \
	  GOOS=linux GOARCH=$$a CGO_ENABLED=0 go build $(GOFLAGS_BUILD) -o "dist/$$p-linux-$$a" "./$$p"; \
	done; done
	install -m 0755 tmux-warp/tmux-warp.sh dist/tmux-warp.sh
	cd dist && sha256sum -- * > SHA256SUMS
	@cat dist/SHA256SUMS

## dev: link the bin/ builds into $(PREFIX); an installed release is kept
## beside it as <name>.release and comes back on undev
dev: build
	@mkdir -p "$(PREFIX)"
	@for p in $(PLUGINS); do t="$(PREFIX)/$$p"; \
	  if [[ -f "$$t" && ! -L "$$t" ]]; then mv -f "$$t" "$$t.release"; fi; \
	  ln -sfn "$(CURDIR)/bin/$$p" "$$t"; echo "linked $$t"; \
	done

## undev: remove the links to this checkout and put kept releases back
undev:
	@for p in $(PLUGINS); do t="$(PREFIX)/$$p"; \
	  if [[ -L "$$t" && "$$(readlink "$$t")" == "$(CURDIR)/bin/$$p" ]]; then rm -f "$$t"; echo "unlinked $$t"; fi; \
	  if [[ ! -e "$$t" && -f "$$t.release" ]]; then mv -f "$$t.release" "$$t"; echo "restored $$t"; fi; \
	done

## release: tag V (vX.Y.Z) on an up-to-date, clean main after the tests
## pass, and push the tag; CI builds and publishes the release
release:
	@[[ "$(V)" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$$ ]] || { echo "usage: make release V=vX.Y.Z" >&2; exit 2; }
	@[[ "$$(git rev-parse --abbrev-ref HEAD)" == main ]] || { echo "release: not on main" >&2; exit 1; }
	@[[ -z "$$(git status --porcelain)" ]] || { echo "release: working tree not clean" >&2; exit 1; }
	git fetch --quiet --tags origin main
	@[[ "$$(git rev-parse HEAD)" == "$$(git rev-parse origin/main)" ]] || { echo "release: main differs from origin/main; push or pull first" >&2; exit 1; }
	@! git rev-parse -q --verify "refs/tags/$(V)" >/dev/null || { echo "release: $(V) exists" >&2; exit 1; }
	@last=$$(git tag -l 'v*' --sort=-v:refname | head -1); \
	  [[ -z "$$last" || "$$(printf '%s\n%s\n' "$$last" "$(V)" | sort -V | tail -1)" == "$(V)" ]] || { echo "release: $(V) is not after $$last" >&2; exit 1; }
	$(MAKE) test
	git tag -a "$(V)" -m "$(V)"
	git push origin "$(V)"

clean:
	rm -rf bin dist
