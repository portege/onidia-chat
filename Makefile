.PHONY: build run preview clean agentctl pack
.PHONY: test
test:
	go test -count=1 ./...


build:
	go build -trimpath -ldflags="-s -w" -o chat-app .

agentctl:
	go build -trimpath -ldflags="-s -w" -o agentctl ./cmd/agentctl

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
	rm -f chat-app chat_ui_*.png
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


