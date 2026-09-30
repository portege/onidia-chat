.PHONY: build run preview clean agentctl pack dist deb
.PHONY: test
test:
	go test -count=1 ./...


build:
	go build -trimpath -ldflags="-s -w" -o chat-app .
	go build -trimpath -ldflags="-s -w" -o preflight ./cmd/preflight

agentctl:
	go build -trimpath -ldflags="-s -w" -o agentctl ./cmd/agentctl

# Requirements check (the standalone preflight): is the selected LLM backend
# reachable/credentialed and the environment sane BEFORE launching? Runs the
# same checks chat-app's startup gate runs (strict by default there).
# Exit codes: 0 ok, 1 warnings only, 2 blocked. Extras via ARGS, e.g.:
#   make preflight ARGS="-all -json"    # probe every backend, machine-readable
preflight:
	go run ./cmd/preflight $(ARGS)

# Alias: same checks, "doctor" naming.
doctor: preflight

run:
	./chat-app

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
	rm -f chat-app preflight chat_ui_*.png
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
ARCH       := $(shell uname -m)
DIST       := dist

# The shipped example config is the checked-in template with two keys relaxed:
# provider=gemini and preflight=warn. The template targets your own setup
# (bedrock + strict), and strict preflight exits 2 on a machine with no
# credentials yet - so a fresh install would refuse to open its window before
# anyone had a chance to paste a key into it.
$(DIST)/chat-app.ini.example: chat-app.ini.template
	@mkdir -p $(DIST)
	sed -e 's/^provider = .*/provider = gemini/' -e 's/^preflight = .*/preflight = warn/' $< > $@

# Release tarball. `pack` first, so the agents ship as zips the bundled
# install.sh can register with agentctl on the target machine.
dist: build agentctl pack $(DIST)/chat-app.ini.example
	../packaging/mktar.sh --name chat-app --version $(VERSION) --arch $(ARCH) --out $(DIST) \
		--file chat-app:bin/chat-app:0755 \
		--file preflight:bin/preflight:0755 \
		--file agentctl:bin/agentctl:0755 \
		--file $(DIST)/chat-app.ini.example:share/chat-app.ini.example:0644 \
		--file README.md:share/README.md:0644 \
		--file ../packaging/install.sh:install.sh:0755 \
		--file ../packaging/desktop/chat-app.desktop:share/chat-app.desktop:0644 \
		--file ../DISTRIBUTING.md:DISTRIBUTING.md:0644 \
		--file ../RELEASE.md:RELEASE.md:0644 \
		--dir ../packaging/icons:share/icons \
		--dir $(DIST)/agents:agents

# Debian package. The config example lands next to the binary in /opt so it is
# found but NOT auto-loaded (chat-app only auto-loads ./chat-app.ini); copying
# it to ~/chat-app.ini is the user's one deliberate step.
deb: build agentctl pack $(DIST)/chat-app.ini.example
	@mkdir -p $(DIST)
	sed 's|Exec=__BIN__/|Exec=|' ../packaging/desktop/chat-app.desktop > $(DIST)/chat-app.desktop
	../packaging/mkdeb.sh --name chat-app --version $(VERSION) --arch $(ARCH) \
		--maintainer "$(MAINTAINER)" \
		--section utils \
		--homepage https://github.com/portege/onidia-chat \
		--description "Gemini-backed chat window with a desktop-pet voice, speech to text and pluggable agents (pure Go, raw X11)" \
		--out $(DIST)/deb \
		--file chat-app:opt/chat-app/bin/chat-app:0755 \
		--file preflight:opt/chat-app/bin/preflight:0755 \
		--file agentctl:opt/chat-app/bin/agentctl:0755 \
		--file $(DIST)/chat-app.ini.example:opt/chat-app/bin/chat-app.ini.example:0644 \
		--dir $(DIST)/agents:opt/chat-app/agents \
		--file README.md:usr/share/doc/chat-app/README.md:0644 \
		--file ../DISTRIBUTING.md:usr/share/doc/chat-app/DISTRIBUTING.md:0644 \
		--file ../RELEASE.md:usr/share/doc/chat-app/RELEASE.md:0644 \
		--file $(DIST)/chat-app.desktop:usr/share/applications/chat-app.desktop:0644 \
		--file ../packaging/icons/chat-app.svg:usr/share/icons/hicolor/scalable/apps/chat-app.svg:0644 \
		--link /opt/chat-app/bin/chat-app:usr/bin/chat-app \
		--link /opt/chat-app/bin/preflight:usr/bin/preflight \
		--link /opt/chat-app/bin/agentctl:usr/bin/agentctl
