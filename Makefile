.PHONY: build desktop-build desktop-run install safe-install check-forward-only check-version-tag check-install-path clean test test-makefile test-e2e-container check-up-to-date hooks hooks-test verify verify-integration

BINARY := gt
BINARY_DESKTOP := gt-desktop
BUILD_DIR := .
INSTALL_DIR := $(HOME)/.local/bin
E2E_IMAGE ?= gastown-test
E2E_BUILD_FLAGS ?=
E2E_RUN_FLAGS ?= --rm
E2E_BUILD_RETRIES ?= 1
E2E_RUN_RETRIES ?= 1

# Get version info for ldflags
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

LDFLAGS := -s -w \
           -X github.com/steveyegge/gastown/internal/cmd.Version=$(VERSION) \
           -X github.com/steveyegge/gastown/internal/cmd.Commit=$(COMMIT) \
           -X github.com/steveyegge/gastown/internal/cmd.BuildTime=$(BUILD_TIME) \
           -X github.com/steveyegge/gastown/internal/cmd.BuiltProperly=1

# ICU4C detection for macOS (required by go-icu-regex transitive dependency).
# Homebrew installs icu4c as a keg-only package, so headers/libs aren't on the
# default search path. Auto-detect the prefix and export CGo flags.
ifeq ($(shell uname),Darwin)
  ICU_PREFIX := $(shell brew --prefix icu4c 2>/dev/null)
  ifneq ($(ICU_PREFIX),)
    export CGO_CPPFLAGS += -I$(ICU_PREFIX)/include
    export CGO_LDFLAGS  += -L$(ICU_PREFIX)/lib
  endif
endif

build:
	go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)-proxy-server ./cmd/gt-proxy-server
	go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)-proxy-client ./cmd/gt-proxy-client
	go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY) ./cmd/gt

desktop-build:
	go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_DESKTOP) ./cmd/gt-desktop

desktop-run:
	go run ./cmd/gt-desktop

check-up-to-date:
ifndef SKIP_UPDATE_CHECK
	@# Skip check on detached HEAD (tag checkouts, CI builds)
	@if ! git symbolic-ref HEAD >/dev/null 2>&1; then exit 0; fi
	@# Use the current branch's tracking ref (works for main, carry/operational, etc.)
	@UPSTREAM=$$(git rev-parse --abbrev-ref --symbolic-full-name @{u} 2>/dev/null); \
	if [ -z "$$UPSTREAM" ]; then \
		echo "Warning: no upstream tracking branch set, skipping update check"; \
		exit 0; \
	fi; \
	REMOTE_NAME=$$(echo "$$UPSTREAM" | cut -d/ -f1); \
	REMOTE_BRANCH=$$(echo "$$UPSTREAM" | cut -d/ -f2-); \
	git fetch "$$REMOTE_NAME" "$$REMOTE_BRANCH" --quiet 2>/dev/null || true; \
	LOCAL=$$(git rev-parse HEAD 2>/dev/null); \
	REMOTE=$$(git rev-parse "$$UPSTREAM" 2>/dev/null); \
	if [ -n "$$REMOTE" ] && [ "$$LOCAL" != "$$REMOTE" ]; then \
		echo "ERROR: Local branch is not up to date with $$UPSTREAM"; \
		echo "  Local:  $$(git rev-parse --short HEAD)"; \
		echo "  Remote: $$(git rev-parse --short $$UPSTREAM)"; \
		echo "Run 'git pull' first, or use SKIP_UPDATE_CHECK=1 to override"; \
		exit 1; \
	fi
endif

# check-forward-only: Ensure HEAD is a descendant of the currently installed binary's commit.
# Prevents rebuilding to an older or diverged commit, which caused a crash loop where
# the replaced binary broke session startup hooks → witness respawned → loop every 1-2 min.
check-forward-only:
ifndef SKIP_FORWARD_CHECK
	@BINARY_COMMIT=$$($(INSTALL_DIR)/$(BINARY) version --verbose 2>/dev/null | grep -o '@[a-f0-9]*' | head -1 | tr -d '@'); \
	if [ -n "$$BINARY_COMMIT" ] && [ "$$BINARY_COMMIT" != "unknown" ]; then \
		HEAD_COMMIT=$$(git rev-parse HEAD 2>/dev/null); \
		if [ "$$BINARY_COMMIT" = "$$HEAD_COMMIT" ] || [ "$$(git rev-parse --short HEAD)" = "$$BINARY_COMMIT" ]; then \
			echo "Binary is already at HEAD, nothing to do"; \
			exit 1; \
		fi; \
		if ! git merge-base --is-ancestor "$$BINARY_COMMIT" HEAD 2>/dev/null; then \
			echo "ERROR: HEAD ($$(git rev-parse --short HEAD)) is NOT a descendant of installed binary ($$BINARY_COMMIT)"; \
			echo "This would be a DOWNGRADE. Refusing to rebuild."; \
			echo "Use SKIP_FORWARD_CHECK=1 to override (dangerous)."; \
			exit 1; \
		fi; \
		echo "Forward-only check passed: $$BINARY_COMMIT → $$(git rev-parse --short HEAD)"; \
	else \
		echo "Warning: cannot determine installed binary commit, skipping forward check"; \
	fi
endif

check-install-path:
	@resolved=$$(command -v $(BINARY) 2>/dev/null || true); \
	if [ "$$resolved" != "$(INSTALL_DIR)/$(BINARY)" ]; then \
		echo "Warning: $(BINARY) resolves to $${resolved:-nothing in PATH}, not $(INSTALL_DIR)/$(BINARY)"; \
		echo "  Add this before other PATH entries in your shell profile:"; \
		echo '  export PATH="$(INSTALL_DIR):$$PATH"'; \
	fi

install: check-up-to-date build
	@mkdir -p $(INSTALL_DIR)
	@rm -f $(INSTALL_DIR)/$(BINARY)
	@cp $(BUILD_DIR)/$(BINARY) $(INSTALL_DIR)/$(BINARY)
	@# Nuke any stale binaries that shadow the canonical location on PATH.
	@# Covers go-install defaults and mise-managed Go installs (any version).
	@# gu-wcxv: without the mise path, PATH-resolved `gt` could stay on the
	@# old binary after install, defeating the rebuild.
	@# gu-ep7f: after removing, symlink back so agent configs referencing the
	@# mise path still resolve to the canonical binary.
	@for bad in $(HOME)/go/bin/$(BINARY) $(HOME)/bin/$(BINARY) \
	            $(HOME)/.local/share/mise/installs/go/*/bin/$(BINARY); do \
		if [ -f "$$bad" ] && [ ! -L "$$bad" ]; then \
			echo "Removing stale $$bad (use make install, not go install)"; \
			rm -f "$$bad"; \
			ln -sf $(INSTALL_DIR)/$(BINARY) "$$bad"; \
		elif [ -L "$$bad" ]; then \
			ln -sf $(INSTALL_DIR)/$(BINARY) "$$bad"; \
		fi; \
	done
	@echo "Installed $(BINARY) to $(INSTALL_DIR)/$(BINARY)"
	@$(MAKE) --no-print-directory check-install-path
	@# Restart daemon so it picks up the new binary.
	@# A stale daemon is a recurring source of bugs (wrong session prefixes, etc.)
	@if $(INSTALL_DIR)/$(BINARY) daemon status >/dev/null 2>&1; then \
		echo "Restarting daemon to pick up new binary..."; \
		$(INSTALL_DIR)/$(BINARY) daemon stop >/dev/null 2>&1 || true; \
		sleep 1; \
		$(INSTALL_DIR)/$(BINARY) daemon start >/dev/null 2>&1 && \
			echo "Daemon restarted." || \
			echo "Warning: daemon restart failed (start manually with: gt daemon start)"; \
	fi
	@# Sync plugins from build repo to town runtime directories.
	@# Prevents drift when plugin fixes merge but runtime dirs are stale.
	@$(INSTALL_DIR)/$(BINARY) plugin sync --source $(CURDIR)/plugins 2>/dev/null && \
		echo "Plugins synced." || true

# safe-install: Replace binary WITHOUT restarting daemon or killing sessions.
# Use this for automated rebuilds (e.g., rebuild-gt plugin). Sessions pick up
# the new binary on their next natural cycle/handoff.
safe-install: check-up-to-date check-forward-only build
	@mkdir -p $(INSTALL_DIR)
	@# Atomic-ish replace: copy to temp then move (move is atomic on same filesystem)
	@cp $(BUILD_DIR)/$(BINARY) $(INSTALL_DIR)/$(BINARY).new
	@mv $(INSTALL_DIR)/$(BINARY).new $(INSTALL_DIR)/$(BINARY)
	@# Nuke any stale binaries that shadow the canonical location on PATH.
	@# Covers go-install defaults and mise-managed Go installs (any version).
	@# gu-wcxv: without the mise path, PATH-resolved `gt` could stay on the
	@# old binary after install, defeating the rebuild.
	@# gu-ep7f: after removing, symlink back so agent configs referencing the
	@# mise path still resolve to the canonical binary.
	@for bad in $(HOME)/go/bin/$(BINARY) $(HOME)/bin/$(BINARY) \
	            $(HOME)/.local/share/mise/installs/go/*/bin/$(BINARY); do \
		if [ -f "$$bad" ] && [ ! -L "$$bad" ]; then \
			echo "Removing stale $$bad (use make install, not go install)"; \
			rm -f "$$bad"; \
			ln -sf $(INSTALL_DIR)/$(BINARY) "$$bad"; \
		elif [ -L "$$bad" ]; then \
			ln -sf $(INSTALL_DIR)/$(BINARY) "$$bad"; \
		fi; \
	done
	@echo "Installed $(BINARY) to $(INSTALL_DIR)/$(BINARY) (daemon NOT restarted)"
	@$(MAKE) --no-print-directory check-install-path
	@echo "Sessions will pick up new binary on next cycle."

# check-version-tag: Verify that if HEAD is tagged vX.Y.Z, the Version constant
# in internal/cmd/version.go equals X.Y.Z. No-op when HEAD is untagged, so it is
# safe to run on every build but only fails release tag checkouts.
# Prevents recurrence of gh#3459 (v0.13.0 shipped reporting 0.12.1).
check-version-tag:
	@TAG=$$(git describe --tags --exact-match HEAD 2>/dev/null || true); \
	if [ -z "$$TAG" ]; then \
		echo "check-version-tag: HEAD is not a release tag, skipping"; \
		exit 0; \
	fi; \
	case "$$TAG" in \
		v[0-9]*) TAG_VERSION=$${TAG#v} ;; \
		*) echo "check-version-tag: tag '$$TAG' is not a vX.Y.Z release tag, skipping"; exit 0 ;; \
	esac; \
	CODE_VERSION=$$(grep -E '^[[:space:]]*Version[[:space:]]*=[[:space:]]*"' internal/cmd/version.go | head -1 | sed 's/.*"\([^"]*\)".*/\1/'); \
	if [ -z "$$CODE_VERSION" ]; then \
		echo "ERROR: could not parse Version from internal/cmd/version.go"; \
		exit 1; \
	fi; \
	if [ "$$TAG_VERSION" != "$$CODE_VERSION" ]; then \
		echo "ERROR: version mismatch between git tag and Version constant"; \
		echo "  git tag at HEAD:          $$TAG (expects Version=$$TAG_VERSION)"; \
		echo "  internal/cmd/version.go:  Version=$$CODE_VERSION"; \
		echo ""; \
		echo "Run scripts/bump-version.sh before tagging, or re-tag HEAD correctly."; \
		echo "See gh#3459 for background."; \
		exit 1; \
	fi; \
	echo "check-version-tag: OK (tag $$TAG matches Version=$$CODE_VERSION)"

clean:
	rm -f $(BUILD_DIR)/$(BINARY)

test-makefile:
	bash scripts/check-install-path_test.sh
	bash scripts/check-lockfile-mirror_test.sh

# verify: Run the same gates CI's "Test" job runs, locally, before pushing.
# This is the last line of defense before changes reach origin/main. Identical
# to what the .githooks/pre-push hook runs on every push. Env vars that could
# mask test failures (GT_TOWN_ROOT, GT_ROOT) are unset so tests run with a
# clean env matching CI. Does not run `-tags=integration` tests; use
# `make verify-integration` for those (requires Docker).
#
# Usage:
#   make verify                # before pushing, or as a sanity check
#   GT_SKIP_PREPUSH=1 git push # emergency escape if verify is broken
verify:
	@bash scripts/pre-push-check.sh

# verify-integration: Everything `verify` does, plus the -tags=integration
# tests that CI's "Integration Tests" job runs. Requires Docker (tests spin
# up a dolt container). Takes ~5min.
verify-integration: verify
	@echo "==> verify-integration: running -tags=integration tests (requires Docker)"
	@go test ./... -tags=integration -count=1 -timeout 15m

# Run e2e tests in isolated container (the only supported way to run them)
test-e2e-container:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -Command "$$max=$(E2E_BUILD_RETRIES); for($$i=1; $$i -le $$max; $$i++){ docker build $(E2E_BUILD_FLAGS) -f Dockerfile.e2e -t $(E2E_IMAGE) .; if($$LASTEXITCODE -eq 0){ break }; if($$i -eq $$max){ exit 1 }; Write-Host ('docker build failed (attempt ' + $$i + '), retrying...'); Start-Sleep -Seconds 2 }"
	@powershell -NoProfile -Command "$$max=$(E2E_RUN_RETRIES); for($$i=1; $$i -le $$max; $$i++){ docker run $(E2E_RUN_FLAGS) $(E2E_IMAGE); if($$LASTEXITCODE -eq 0){ break }; if($$i -eq $$max){ exit 1 }; Write-Host ('docker run failed (attempt ' + $$i + '), retrying...'); Start-Sleep -Seconds 2 }"
else
	@attempt=1; \
	while [ $$attempt -le $(E2E_BUILD_RETRIES) ]; do \
		docker build $(E2E_BUILD_FLAGS) -f Dockerfile.e2e -t $(E2E_IMAGE) . && break; \
		if [ $$attempt -eq $(E2E_BUILD_RETRIES) ]; then exit 1; fi; \
		echo "docker build failed (attempt $$attempt), retrying..."; \
		attempt=$$((attempt+1)); \
		sleep 2; \
	done
	@attempt=1; \
	while [ $$attempt -le $(E2E_RUN_RETRIES) ]; do \
		docker run $(E2E_RUN_FLAGS) $(E2E_IMAGE) && break; \
		if [ $$attempt -eq $(E2E_RUN_RETRIES) ]; then exit 1; fi; \
		echo "docker run failed (attempt $$attempt), retrying..."; \
		attempt=$$((attempt+1)); \
		sleep 2; \
	done
endif

# hooks: Wire this clone's git to use the repo-tracked hooks in .githooks/.
# Idempotent — safe to re-run. `gt doctor` (hooks-path-all-rigs check) also
# fixes this with `gt doctor --fix`; this target is the manual path.
hooks:
	@git config core.hooksPath .githooks
	@echo "core.hooksPath set to .githooks for $$(git rev-parse --show-toplevel)"
	@ls -1 .githooks | grep -v '_test\.sh$$' | sed 's/^/  - /'

# hooks-test: Run the test suites shipped alongside each hook.
hooks-test:
	@fail=0; \
	for t in .githooks/*_test.sh; do \
		[ -f "$$t" ] || continue; \
		echo "=== $$t ==="; \
		bash "$$t" || fail=1; \
	done; \
	exit $$fail
