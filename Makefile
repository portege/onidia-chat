.PHONY: build run preview clean agentctl pack dist deb
.PHONY: test
.PHONY: archinfo

# Cross-compilation. Default is this machine (a Pi 5, so arm64). Set
# GOARCH=arm GOARM=7 for the 32-bit ARM boards (Pi Zero/1, NanoPi NEO Core
# v1.1 - Allwinner H3 Cortex-A7); `make dist deb TARGET=armhf` sets both.
# CGO_ENABLED=0 keeps the binary static: no cgo in this project, and it drops
# the glibc-version floor so one build runs on any Linux of the same arch.
export CGO_ENABLED ?= 0
test:
	go test -count=1 ./...


build:
	go build -trimpath -ldflags="-s -w" -o onidia-chat .
	go build -trimpath -ldflags="-s -w" -o preflight ./cmd/preflight

agentctl:
	go build -trimpath -ldflags="-s -w" -o agentctl ./cmd/agentctl

# Requirements check (the standalone preflight): is the selected LLM backend
# reachable/credentialed and the environment sane BEFORE launching? Runs the
# same checks onidia-chat's startup gate runs (strict by default there).
# Exit codes: 0 ok, 1 warnings only, 2 blocked. Extras via ARGS, e.g.:
#   make preflight ARGS="-all -json"    # probe every backend, machine-readable
preflight:
	go run ./cmd/preflight $(ARGS)

# Alias: same checks, "doctor" naming.
doctor: preflight

run:
	./onidia-chat

# Headless UI previews: renders chat_ui_*.png sample states and exits (same
# idea as the desktop-pet's `make debug`).
preview:
	go run . -preview

# Debug the Gemini API separately from the UI: list the models your key can
# use, or fire one test completion. Pass extra args, e.g.
#   make test-api ARGS="-models"
#   make test-api ARGS="-model gemini-2.0-flash tell me a joke"
test-api:
	go run ./cmd/geminitest $(ARGS)

clean:
	rm -f onidia-chat preflight chat_ui_*.png
	rm -rf dist

# Zip every shippable agent in agents/<name> into dist/agents/<name>.zip
# for distribution and agentctl install. Skips template/private folders
# (leading "_", same rule as discovery) and tolerates an empty agents dir.
pack:
	@mkdir -p dist/agents
	@for d in agents/*/ ; do \
		[ -d "$$d" ] || continue ; \
		case "$$d" in */_*) continue ;; esac ; \
		n=$$(basename $$d) ; \
		( cd agents && zip -qr ../dist/agents/$$n.zip $$n ) ; \
		echo "dist/agents/$$n.zip" ; \
	done

# ---- release packaging (see ../DISTRIBUTING.md) -------------------------------
# Version and maintainer come from the top-level Makefile by default.
VERSION    ?= 1.0.0
MAINTAINER ?= Boyke <boi@babeh.com>
# ARCH names the artifact. GOARCH/GOARM must ALSO reach `go build` through the
# ENVIRONMENT, and that is the part that is easy to get wrong: dist/deb depend
# on this Makefile's own `build`, which is invoked with no GOARCH override, so
# setting ARCH alone produces a native aarch64 binary wearing an armhf name.
#   (nothing set)  native, e.g. aarch64 on this Pi 5
#   TARGET=armhf   32-bit ARMv7 (Pi Zero 2W, NanoPi NEO Core v1.1) -> armhf
#   TARGET=armel   32-bit ARMv6 (original Pi Zero / Pi 1, BCM2835) -> armel
#   TARGET=arm64   64-bit ARM
#   TARGET=amd64   x86-64 desktops
TARGET     ?=
ifeq ($(TARGET),armhf)
ARCH := armv7l
export GOARCH := arm
export GOARM := 7
else ifeq ($(TARGET),armel)
ARCH := armv6l
export GOARCH := arm
export GOARM := 6
else ifeq ($(TARGET),arm64)
ARCH := aarch64
export GOARCH := arm64
else ifeq ($(TARGET),amd64)
ARCH := x86_64
export GOARCH := amd64
else
ARCH := $(shell uname -m)
endif
DIST       := dist

archinfo:
	@echo "host      : $$(uname -m)"
	@echo "TARGET    : $(TARGET)"
	@echo "artifact  : $(ARCH)"
	@echo "CGO       : $(CGO_ENABLED)"

# The shipped config is the checked-in template with three things done to it:
#   provider=gemini and preflight=warn, because the template targets your own
#   setup (bedrock + strict) and strict preflight exits 2 on a machine with no
#   credentials yet - a fresh install would refuse to open its window before
#   anyone had a chance to add a key.
#   any api-key line is blanked, so a key that was ever pasted into the
#   template by accident cannot ride along in a published package.
# Built fresh every time (not a file target) so an edited template is always
# picked up, and so the key cannot survive in a stale dist/.
$(DIST)/chat-app.ini: chat-app.ini.template
	@mkdir -p $(DIST)
	sed -e 's/^provider = .*/provider = gemini/' \
	    -e 's/^preflight = .*/preflight = warn/' \
	    -e 's/^\(api-key[[:space:]]*=[[:space:]]*\).*/\1/' $< > $@
	@rm -f $(DIST)/chat-app.ini.example

# Release tarball. `pack` first, so the agents ship as zips the bundled
# install.sh can register with agentctl on the target machine.
# What actually ships: onidia-chat and chat-app.ini. preflight is a standalone
# diagnostic (onidia-chat runs the same checks internally at startup), agentctl
# only installs the downloadable agents, and the agent zips are python scripts
# the built-in story agent does not need. Kept building locally under `make
# build`/`make agentctl`, just not distributed.
dist: build $(DIST)/chat-app.ini
	../packaging/mktar.sh --name onidia-chat --version $(VERSION) --arch $(ARCH) --out $(DIST) \
		--file onidia-chat:bin/onidia-chat:0755 \
		--file $(DIST)/chat-app.ini:share/chat-app.ini:0644 \
		--file README.md:share/README.md:0644 \
		--file ../packaging/install.sh:install.sh:0755 \
		--file ../packaging/desktop/onidia-chat.desktop:share/onidia-chat.desktop:0644 \
		--file ../DISTRIBUTING.md:DISTRIBUTING.md:0644 \
		--file ../RELEASE.md:RELEASE.md:0644 \
		--dir ../packaging/icons:share/icons

# Debian package. The config example lands next to the binary in /opt so it is
# found but NOT auto-loaded (onidia-chat only auto-loads ./chat-app.ini); copying
# it to ~/.config/chat-app/chat-app.ini is the user's one deliberate step.
deb: build $(DIST)/chat-app.ini
	@mkdir -p $(DIST)
	sed 's|Exec=__BIN__/|Exec=|' ../packaging/desktop/onidia-chat.desktop > $(DIST)/onidia-chat.desktop
	../packaging/mkdeb.sh --name onidia-chat --version $(VERSION) --arch $(ARCH) \
		--maintainer "$(MAINTAINER)" \
		--section utils \
		--homepage https://github.com/portege/onidia-chat \
		--description "Chat window with a desktop-pet voice and speech to text (pure Go, raw X11)" \
		--out $(DIST)/deb \
		--file onidia-chat:opt/onidia-chat/bin/onidia-chat:0755 \
		--file $(DIST)/chat-app.ini:opt/onidia-chat/bin/chat-app.ini:0644 \
		--file README.md:usr/share/doc/onidia-chat/README.md:0644 \
		--file ../DISTRIBUTING.md:usr/share/doc/onidia-chat/DISTRIBUTING.md:0644 \
		--file ../RELEASE.md:usr/share/doc/onidia-chat/RELEASE.md:0644 \
		--file $(DIST)/onidia-chat.desktop:usr/share/applications/onidia-chat.desktop:0644 \
		--file ../packaging/icons/onidia-chat.svg:usr/share/icons/hicolor/scalable/apps/onidia-chat.svg:0644 \
		--link /opt/onidia-chat/bin/onidia-chat:usr/bin/onidia-chat
