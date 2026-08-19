# Local LLM Chat - Makefile

BINARY_NAME=llm-chat
LLAMA_CPP_PATH=$(HOME)/llama.cpp/build/bin
PORT=3000
MODELS_DIR=$(abspath ../models)

# Build the application
build:
	go build -o $(BINARY_NAME) main.go
	@echo "✅ Built $(BINARY_NAME)"

# Run the application
run: build
	PATH="$(LLAMA_CPP_PATH):$(PATH)" ./$(BINARY_NAME) --models-dir "$(MODELS_DIR)"

# Run in development mode (with go run)
dev:
	PATH="$(LLAMA_CPP_PATH):$(PATH)" go run main.go --models-dir "$(MODELS_DIR)"

# Clean build artifacts
clean:
	rm -f $(BINARY_NAME)
	@echo "✅ Cleaned"

# Format code
fmt:
	go fmt ./...
	@echo "✅ Formatted"

# Run tests
test:
	go test -v ./...

# Check if server is running
status:
	@curl -s http://localhost:$(PORT)/api/status | python3 -m json.tool || echo "Server not running"

# Stop the server
stop:
	@pkill -f $(BINARY_NAME) 2>/dev/null && echo "✅ Stopped" || echo "Not running"

# Build llama.cpp (if not already built)
llama-cpp:
	@if [ ! -f $(LLAMA_CPP_PATH)/llama-server ]; then \
		echo "Building llama.cpp..."; \
		cd $(HOME)/llama.cpp && cmake -B build && cmake --build build --config Release -j$$(nproc); \
	else \
		echo "✅ llama-server already built"; \
	fi

# Install everything
install: llama-cpp build
	@echo "✅ Installation complete"
	@echo "Run: PATH=\"$(LLAMA_CPP_PATH):$(PATH)\" ./$(BINARY_NAME)"

# Show help
help:
	@echo "Local LLM Chat - Makefile Commands"
	@echo "==================================="
	@echo "make build      - Build the binary"
	@echo "make run        - Build and run"
	@echo "make dev        - Run with go run (development)"
	@echo "make clean      - Remove build artifacts"
	@echo "make fmt        - Format Go code"
	@echo "make test       - Run tests"
	@echo "make status     - Check server status"
	@echo "make stop       - Stop the server"
	@echo "make llama-cpp  - Build llama.cpp if needed"
	@echo "make install    - Install everything"
	@echo "make help       - Show this help"

.PHONY: build run dev clean fmt test status stop llama-cpp install help
