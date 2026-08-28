.PHONY: build run preview clean

build:
	go build -trimpath -ldflags="-s -w" -o chat-app .

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
