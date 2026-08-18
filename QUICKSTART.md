# 🚀 Quick Start Guide

Get up and running in 5 minutes.

---

## Step 1: Verify Prerequisites

```bash
# Check Go is installed
go version
# Expected: go version go1.21+ linux/amd64

# Check llama-server is available
which llama-server || ls ~/llama.cpp/build/bin/llama-server
```

If llama-server is missing, build it:
```bash
git clone https://github.com/ggerganov/llama.cpp ~/llama.cpp
cd ~/llama.cpp
cmake -B build
cmake --build build --config Release -j$(nproc)
```

---

## Step 2: Verify Models Exist

```bash
ls -la ~/Downloads/models/phi4-mini/*.gguf
ls -la ~/Downloads/models/mistral-7b/*.gguf
```

If missing, download them:
```bash
hf download bartowski/microsoft_Phi-4-mini-instruct-GGUF \
  --include "microsoft_Phi-4-mini-instruct-Q4_K_M.gguf" \
  --local-dir ~/Downloads/models/phi4-mini

hf download bartowski/Mistral-7B-Instruct-v0.3-GGUF \
  --include "Mistral-7B-Instruct-v0.3-Q4_K_M.gguf" \
  --local-dir ~/Downloads/models/mistral-7b
```

---

## Step 3: Start the Server

```bash
cd ~/Downloads/models/llm-chat
PATH="$HOME/llama.cpp/build/bin:$PATH" ./llm-chat
```

You should see:
```
2024/01/15 10:00:00 Starting server on :3000
2024/01/15 10:00:00 llama-server binary: /home/dadi/llama.cpp/build/bin/llama-server
2024/01/15 10:00:00 Loaded 2 model configs
```

---

## Step 4: Open the Interface

Open your browser and go to:
```
http://localhost:3000
```

---

## Step 5: Load a Model and Chat

1. Select "Phi-4 Mini Instruct" from the dropdown
2. Click "Load Model"
3. Wait 10-30 seconds (watch the green status dot)
4. Type a message and press Enter
5. Watch the response stream in real-time!

---

## Stopping the Server

Press `Ctrl+C` in the terminal, or:
```bash
pkill -f llm-chat
```

---

## Common Commands

```bash
# Start in background
nohup ./llm-chat > /tmp/llm-chat.log 2>&1 &

# Check if running
curl http://localhost:3000/api/status

# View logs
tail -f /tmp/llm-chat.log

# Stop background server
pkill -f llm-chat
```
