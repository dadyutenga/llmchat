# 🤖 Local LLM Chat

A lightweight, self-hosted web application for running and chatting with local GGUF language models. Built with a Go backend and vanilla HTML/CSS/JS frontend — no frameworks, no Docker, no cloud dependencies.

---

## 📋 Table of Contents

- [Features](#-features)
- [Architecture](#-architecture)
- [Prerequisites](#-prerequisites)
- [Installation](#-installation)
  - [1. Install Go](#1-install-go)
  - [2. Build llama.cpp](#2-build-llamacpp)
  - [3. Download Models](#3-download-models)
  - [4. Setup the Application](#4-setup-the-application)
- [Configuration](#-configuration)
  - [models.json](#modelsjson)
  - [Model Fields](#model-fields)
  - [Adding New Models](#adding-new-models)
- [Usage](#-usage)
  - [Starting the Server](#starting-the-server)
  - [Using the Web Interface](#using-the-web-interface)
  - [Agent Mode](#agent-mode)
  - [API Reference](#api-reference)
- [Project Structure](#-project-structure)
- [Troubleshooting](#-troubleshooting)
- [Performance Tips](#-performance-tips)
- [Security Notes](#-security-notes)
- [License](#-license)

---

## ✨ Features

- **Model Management** — Load/unload models on demand, one at a time to conserve RAM
- **Streaming Chat** — Real-time token-by-token response streaming via Server-Sent Events (SSE)
- **Agent Mode** — ReAct-style reasoning loop with `web_search` and `fetch_url` tools for questions needing current information
- **Web Search** — Built-in DuckDuckGo search integration (no API key required)
- **SSRF Protection** — Agent's `fetch_url` blocks private/loopback IPs to prevent self-targeting
- **Dark Theme UI** — Clean, minimal interface that's easy on the eyes
- **Zero Dependencies** — No npm, no webpack, no Docker. Just Go + static HTML
- **Extensible** — Add new models by editing a single JSON file
- **Graceful Cleanup** — Properly kills child processes on exit (Ctrl+C)
- **Health Monitoring** — Polls llama-server until model is fully loaded before enabling chat
- **Process Management** — Automatic cleanup of previous model when switching

---

## 🏗️ Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                     Browser (Frontend)                       │
│  ┌─────────────────────────────────────────────────────┐    │
│  │  index.html (Vanilla JS + CSS)                      │    │
│  │  - Model selector dropdown                          │    │
│  │  - Chat interface with streaming display            │    │
│  │  - Dark theme, responsive layout                    │    │
│  └─────────────────────────────────────────────────────┘    │
└──────────────────────────┬──────────────────────────────────┘
                           │ HTTP (port 3000)
┌──────────────────────────▼──────────────────────────────────┐
│                    Go Backend (main.go)                       │
│  ┌─────────────────────────────────────────────────────┐    │
│  │  API Endpoints:                                     │    │
│  │  GET  /api/models  → List models + status           │    │
│  │  GET  /api/status   → Current active model          │    │
│  │  POST /api/load     → Load/unload model             │    │
│  │  POST /api/chat     → Send message (SSE stream)     │    │
│  └─────────────────────────────────────────────────────┘    │
│  ┌─────────────────────────────────────────────────────┐    │
│  │  Process Manager:                                   │    │
│  │  - Spawns llama-server as subprocess                │    │
│  │  - Health check polling (/health endpoint)          │    │
│  │  - One model at a time (kill previous)              │    │
│  │  - Graceful shutdown on SIGINT/SIGTERM              │    │
│  └─────────────────────────────────────────────────────┘    │
└──────────────────────────┬──────────────────────────────────┘
                           │ OpenAI-compatible API (dynamic port)
┌──────────────────────────▼──────────────────────────────────┐
│              llama-server (llama.cpp)                         │
│  ┌─────────────────────────────────────────────────────┐    │
│  │  - Loads GGUF model into memory                     │    │
│  │  - Serves /v1/chat/completions (OpenAI format)      │    │
│  │  - Streaming token generation                       │    │
│  │  - Health endpoint at /health                       │    │
│  └─────────────────────────────────────────────────────┘    │
└──────────────────────────┬──────────────────────────────────┘
                           │
                    ┌──────▼──────┐
                    │  GGUF Model │
                    │   (.gguf)   │
                    └─────────────┘
```

**Request Flow:**
1. User selects model → clicks "Load Model"
2. Go backend spawns `llama-server -m <model_path> --port <port>`
3. Backend polls `http://127.0.0.1:<port>/health` until ready
4. User sends message → Go proxies to llama-server's `/v1/chat/completions`
5. Response streams back token-by-token via SSE
6. On model switch: previous llama-server process killed, new one spawned

---

## 📦 Prerequisites

| Requirement | Version | Purpose |
|-------------|---------|---------|
| **Go** | 1.21+ | Backend server |
| **GCC/G++** | Any | Building llama.cpp |
| **CMake** | 3.14+ | Building llama.cpp |
| **Git** | Any | Cloning llama.cpp |
| **RAM** | 8GB+ | Running models (depends on model size) |

---

## 🚀 Installation

### 1. Install Go

```bash
# Fedora/RHEL
sudo dnf install golang

# Ubuntu/Debian
sudo apt install golang-go

# Or download from https://go.dev/dl/
```

Verify:
```bash
go version
```

### 2. Build llama.cpp

```bash
# Clone the repository
git clone https://github.com/ggerganov/llama.cpp
cd llama.cpp

# Build with CMake
cmake -B build
cmake --build build --config Release -j$(nproc)

# Verify the binary
./build/bin/llama-server --version

# Optional: Add to PATH (add to ~/.bashrc for persistence)
export PATH="$HOME/llama.cpp/build/bin:$PATH"
```

### 3. Download Models

Download GGUF quantized models. Here are the models configured by default:

```bash
# Create models directory
mkdir -p ~/Downloads/models

# Phi-4 Mini (2.5GB, Q4_K_M quantization)
hf download bartowski/microsoft_Phi-4-mini-instruct-GGUF \
  --include "microsoft_Phi-4-mini-instruct-Q4_K_M.gguf" \
  --local-dir ~/Downloads/models/phi4-mini

# Mistral 7B v0.3 (4.4GB, Q4_K_M quantization)
hf download bartowski/Mistral-7B-Instruct-v0.3-GGUF \
  --include "Mistral-7B-Instruct-v0.3-Q4_K_M.gguf" \
  --local-dir ~/Downloads/models/mistral-7b
```

**Other recommended models:**
```bash
# Llama 3.2 3B (great for low RAM)
hf download bartowski/Llama-3.2-3B-Instruct-GGUF \
  --include "*Q4_K_M*" \
  --local-dir ~/Downloads/models/llama-3.2-3b

# Qwen 2.5 7B
hf download bartowski/Qwen2.5-7B-Instruct-GGUF \
  --include "*Q4_K_M*" \
  --local-dir ~/Downloads/models/qwen-2.5-7b
```

### 4. Setup the Application

```bash
# Navigate to the app directory
cd ~/Downloads/models/llm-chat

# Build the binary
go build -o llm-chat main.go

# Verify
./llm-chat --help 2>&1 || echo "Ready to run"
```

---

## ⚙️ Configuration

### models.json

The `models.json` file defines all available models. The application reads this on startup.

```json
[
  {
    "id": "phi4-mini",
    "name": "Phi-4 Mini Instruct",
    "path": "/home/dadi/Downloads/models/phi4-mini/microsoft_Phi-4-mini-instruct-Q4_K_M.gguf",
    "port": 8081,
    "ctx_size": 4096
  },
  {
    "id": "mistral-7b",
    "name": "Mistral 7B v0.3",
    "path": "/home/dadi/Downloads/models/mistral-7b/Mistral-7B-Instruct-v0.3-Q4_K_M.gguf",
    "port": 8082,
    "ctx_size": 8192
  }
]
```

### Model Fields

| Field | Type | Description |
|-------|------|-------------|
| `id` | string | Unique identifier (used in API calls) |
| `name` | string | Display name shown in UI |
| `path` | string | Absolute path to the `.gguf` model file |
| `port` | int | Port for llama-server (each model needs a unique port) |
| `ctx_size` | int | Context window size in tokens (higher = more RAM) |

### Adding New Models

1. Download a GGUF model
2. Add an entry to `models.json`:
   ```json
   {
     "id": "my-new-model",
     "name": "My New Model",
     "path": "/path/to/my-model.gguf",
     "port": 8083,
     "ctx_size": 4096
   }
   ```
3. Restart the server — no code changes needed!

**Port allocation:** Use ports 8081, 8082, 8083, etc. Each model needs a unique port.

**Context size guide:**

| ctx_size | RAM Usage | Use Case |
|----------|-----------|----------|
| 2048 | Low | Quick Q&A |
| 4096 | Medium | General chat (recommended) |
| 8192 | High | Long conversations |
| 16384 | Very High | Document analysis |

---

## 💻 Usage

### Starting the Server

```bash
# Method 1: Using go run (development)
cd ~/Downloads/models/llm-chat
PATH="$HOME/llama.cpp/build/bin:$PATH" go run main.go

# Method 2: Using compiled binary (production)
cd ~/Downloads/models/llm-chat
PATH="$HOME/llama.cpp/build/bin:$PATH" ./llm-chat

# Method 3: With PATH in .bashrc (persistent)
echo 'export PATH="$HOME/llama.cpp/build/bin:$PATH"' >> ~/.bashrc
source ~/.bashrc
cd ~/Downloads/models/llm-chat
./llm-chat
```

The server starts on **http://localhost:3000** by default.

### Using the Web Interface

1. **Open browser** → Navigate to `http://localhost:3000`

2. **Select a model** → Use the dropdown at the top

3. **Click "Load Model"** → Wait 10-30 seconds (watch the status indicator)
   - 🟡 Yellow pulsing = Loading
   - 🟢 Green = Ready
   - 🔴 Red = Error

4. **Start chatting** → Type message and press Enter or click Send

5. **Switch models** → Select a different model and click Load (previous model automatically unloaded)

**Keyboard shortcuts:**
- `Enter` — Send message
- `Shift+Enter` — New line in message

### Agent Mode

Agent mode enables the assistant to search the web and fetch URLs to answer questions that need current information.

1. **Enable agent mode** → Toggle the "🔎 Agent" checkbox next to the model selector
2. **Ask a question** → The model will reason step-by-step, using tools as needed
3. **Watch the process** → Search/read steps appear as visual chips above the answer
4. **Get the answer** → The final answer streams token-by-token like normal chat

**How it works:**
- The model uses a ReAct (Reasoning + Acting) loop with up to 5 tool calls
- `web_search` — Searches DuckDuckGo (no API key needed)
- `fetch_url` — Reads a web page's text content
- Tool observations are fed back to the model for continued reasoning
- If the model already knows the answer, it skips tools entirely

**Per-model configuration:**
- Each model has an `agent_capable` flag in `models.json`
- The Agent toggle is automatically disabled for models that can't use it
- All current models are instruct-tuned and support agent mode

### API Reference

All endpoints are served on `http://localhost:3000`.

#### GET /api/models

List all configured models with their current status.

**Response:**
```json
[
  {
    "config": {
      "id": "phi4-mini",
      "name": "Phi-4 Mini Instruct",
      "path": "/home/dadi/Downloads/models/phi4-mini/microsoft_Phi-4-mini-instruct-Q4_K_M.gguf",
      "port": 8081,
      "ctx_size": 4096
    },
    "state": "ready",
    "started": "2024-01-15T10:30:00Z"
  }
]
```

**States:** `unloaded`, `loading`, `ready`, `error`

#### GET /api/status

Get the current active model and all model states.

**Response:**
```json
{
  "active_model": "phi4-mini",
  "models": {
    "phi4-mini": { "state": "ready", "..." : "..." },
    "mistral-7b": { "state": "unloaded", "..." : "..." }
  }
}
```

#### POST /api/load

Load a model into memory. If another model is loaded, it will be unloaded first.

**Request:**
```json
{
  "model_id": "phi4-mini"
}
```

**Response:**
```json
{
  "model": "phi4-mini",
  "status": "loading"
}
```

**Note:** After calling this, poll `/api/status` until state becomes `ready` (typically 10-30 seconds).

#### POST /api/chat

Send a chat message and receive a streaming response.

**Request:**
```json
{
  "model_id": "phi4-mini",
  "messages": [
    { "role": "user", "content": "What is the capital of France?" }
  ]
}
```

**Response:** Server-Sent Events (SSE) stream:
```
data: {"choices":[{"delta":{"content":"The"}}],...}
data: {"choices":[{"delta":{"content":" capital"}}],...}
data: {"choices":[{"delta":{"content":" of"}}],...}
data: {"choices":[{"delta":{"content":" France"}}],...}
data: {"choices":[{"delta":{"content":" is"}}],...}
data: {"choices":[{"delta":{"content":" Paris."}}],...}
data: [DONE]
```

**Error responses:**
- `404` — Model not found
- `503` — Model not loaded (send `/api/load` first)
- `502` — llama-server connection failed

#### POST /api/agent

Run an agent-mode chat with tool use (web search, URL fetching). Uses a ReAct-style reasoning loop.

**Request:**
```json
{
  "model_id": "phi4-mini",
  "messages": [
    { "role": "user", "content": "What's the latest news about Go 1.24?" }
  ]
}
```

**Response:** Server-Sent Events (SSE) stream with named events:
```
event: step
data: {"type":"action","tool":"web_search","input":"Go 1.24 latest news"}

event: step
data: {"type":"observation","tool":"web_search","output":"1. Go 1.24 Released...\n  https://go.dev/...\n  ..."}

event: token
data: {"content":"Go 1.24 was"}

event: token
data: {"content":" released"}

event: done
data: {}
```

**SSE Event Types:**
- `event: step` — Agent action or observation (tool use progress)
- `event: token` — Final answer streaming token
- `event: done` — Stream complete

**Agent Loop:**
1. Model reasons about the question (up to 5 iterations)
2. May call `web_search` or `fetch_url` tools
3. Observations are fed back to the model
4. Final answer is streamed token-by-token

**Error responses:** Same as `/api/chat`.

---

## 📁 Project Structure

```
llm-chat/
├── main.go              # Go backend (API + process management)
├── agent.go             # Agent loop (ReAct-style reasoning with tools)
├── tools.go             # Tool registry (web_search, fetch_url)
├── models.json          # Model configuration file
├── go.mod               # Go module definition
├── go.sum               # Go dependency checksums
├── llm-chat             # Compiled binary (after build)
├── static/
│   └── index.html       # Frontend (HTML + CSS + JS)
└── README.md            # This documentation
```

### File Descriptions

**main.go** (~400 lines)
- HTTP server on port 3000
- Model config loading from JSON
- Subprocess management (spawn/kill llama-server)
- Health check polling
- SSE proxy for streaming chat
- Graceful shutdown handler

**agent.go** (~400 lines)
- ReAct-style reasoning loop (max 5 iterations)
- Parses `Thought:` / `Action:` / `Action Input:` / `Final Answer:` from model output
- Dispatches to registered tools, feeds Observations back
- Streams SSE events (step, token, done) to the frontend
- 2-minute total timeout, 60s per-llama-call, 15s per-tool-call

**tools.go** (~320 lines)
- Tool registry with `BuiltinTools()` returning `web_search` and `fetch_url`
- `web_search`: Scrapes DuckDuckGo HTML endpoint, rate-limited (1 req/sec)
- `fetch_url`: Fetches URL with SSRF protection (blocks private/loopback IPs)
- Both return user-friendly error strings for the agent loop

**models.json**
- Array of model configurations
- Add/remove models without code changes

**static/index.html** (~500 lines)
- Single-file frontend (HTML + embedded CSS + JS)
- Model selector with load button
- Chat interface with message history
- Streaming token display
- Status indicators
- Dark theme

---

## 🔧 Troubleshooting

### Model fails to load

**Symptoms:** Red error state, "Model file not found" message

**Solutions:**
```bash
# Check if model file exists
ls -la /path/to/your/model.gguf

# Verify path in models.json matches exactly
cat models.json | grep path
```

### Timeout loading model

**Symptoms:** Loading state persists for 3+ minutes

**Solutions:**
- Check available RAM: `free -h`
- Try smaller model or reduce `ctx_size`
- Check terminal for llama-server errors

### Port conflict

**Symptoms:** "address already in use" error

**Solutions:**
```bash
# Find what's using the port
lsof -i :8081

# Kill the process
kill -9 <PID>

# Or change port in models.json
```

### llama-server not found

**Symptoms:** "Failed to start llama-server" error

**Solutions:**
```bash
# Check if llama-server is in PATH
which llama-server

# If not, add it
export PATH="$HOME/llama.cpp/build/bin:$PATH"

# Or copy to current directory
cp ~/llama.cpp/build/bin/llama-server ~/Downloads/models/llm-chat/
```

### Chat returns 503 error

**Symptoms:** "Model not loaded" when sending messages

**Solution:** Load the model first via the UI or API:
```bash
curl -X POST http://localhost:3000/api/load \
  -H "Content-Type: application/json" \
  -d '{"model_id": "phi4-mini"}'
```

### Slow response times

**Solutions:**
- Reduce `ctx_size` in models.json
- Use a more quantized model (Q4_K_M is good balance)
- Close other applications to free RAM
- Consider a smaller model (3B vs 7B)

---

## ⚡ Performance Tips

### RAM Requirements

| Model Size | Minimum RAM | Recommended |
|------------|-------------|-------------|
| 1-3B params | 4GB | 8GB |
| 7B params | 8GB | 16GB |
| 13B params | 16GB | 32GB |
| 30B+ params | 32GB | 64GB |

### Quantization Guide

| Type | Size | Quality | Speed |
|------|------|---------|-------|
| Q2_K | Smallest | Low | Fastest |
| Q4_K_M | Small | Good | Fast |
| Q5_K_M | Medium | Better | Medium |
| Q6_K | Large | Great | Slower |
| Q8_0 | Largest | Best | Slowest |

**Recommendation:** Q4_K_M offers the best balance of size, quality, and speed.

### Optimization Flags

Edit `main.go` to add llama-server flags:

```go
args := []string{
    "-m", ms.Config.Path,
    "--port", fmt.Sprintf("%d", ms.Config.Port),
    "-c", fmt.Sprintf("%d", ms.Config.CtxSize),
    "--host", "127.0.0.1",
    "--no-mmap",           // Disable memory mapping
    "--mlock",             // Lock model in RAM
    "-ngl", "99",          // Offload all layers to GPU (if available)
    "--threads", "4",      // Number of CPU threads
}
```

---

## 🔒 Security Notes

- **Local only:** Server binds to `0.0.0.0:3000` by default. For local-only access, change to `127.0.0.1:3000` in `main.go`
- **No authentication:** This is designed for local use. Do not expose to the internet without adding auth
- **No TLS:** HTTP only. Add a reverse proxy (nginx) for HTTPS if needed
- **Process isolation:** llama-server runs as subprocess, inherits parent permissions

---

## 📝 License

This project is provided as-is for personal use. No warranty expressed or implied.

---

## 🙏 Credits

- **llama.cpp** — https://github.com/ggerganov/llama.cpp
- **Hugging Face** — Model hosting and `huggingface-cli`
- **GGUF format** — Efficient model quantization

---

## 📞 Quick Reference

```bash
# Start server
cd ~/Downloads/models/llm-chat
PATH="$HOME/llama.cpp/build/bin:$PATH" ./llm-chat

# Open in browser
xdg-open http://localhost:3000

# Check status
curl http://localhost:3000/api/status

# Load model via API
curl -X POST http://localhost:3000/api/load \
  -H "Content-Type: application/json" \
  -d '{"model_id": "phi4-mini"}'

# Stop server
Ctrl+C  # or
pkill -f llm-chat
```
