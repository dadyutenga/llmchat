#!/bin/bash
# Local LLM Chat - Run Script

set -e

# Colors
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

echo -e "${GREEN}🤖 Local LLM Chat${NC}"
echo "================================"

# Check llama-server
LLAMA_SERVER=$(which llama-server 2>/dev/null || echo "")
if [ -z "$LLAMA_SERVER" ]; then
    if [ -f "$HOME/llama.cpp/build/bin/llama-server" ]; then
        export PATH="$HOME/llama.cpp/build/bin:$PATH"
        echo -e "${GREEN}✅ llama-server found at ~/llama.cpp/build/bin/${NC}"
    else
        echo -e "${RED}❌ llama-server not found!${NC}"
        echo "Please build llama.cpp first:"
        echo "  git clone https://github.com/ggerganov/llama.cpp"
        echo "  cd llama.cpp && cmake -B build && cmake --build build --config Release"
        exit 1
    fi
else
    echo -e "${GREEN}✅ llama-server found at: $LLAMA_SERVER${NC}"
fi

# Check models.json
if [ ! -f "models.json" ]; then
    echo -e "${RED}❌ models.json not found!${NC}"
    exit 1
fi
echo -e "${GREEN}✅ models.json found${NC}"

# Check if port is available
PORT=3000
if lsof -Pi :$PORT -sTCP:LISTEN -t >/dev/null 2>&1 ; then
    echo -e "${YELLOW}⚠️  Port $PORT is already in use${NC}"
    read -p "Kill existing process? (y/n) " -n 1 -r
    echo
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        pkill -f "llm-chat" 2>/dev/null || true
        sleep 1
    else
        echo "Exiting..."
        exit 1
    fi
fi

# Build if needed
if [ ! -f "llm-chat" ] || [ "main.go" -nt "llm-chat" ]; then
    echo -e "${YELLOW}Building...${NC}"
    go build -o llm-chat main.go
    echo -e "${GREEN}✅ Built llm-chat${NC}"
fi

# Run
echo ""
echo -e "${GREEN}🚀 Starting server on http://localhost:$PORT${NC}"
echo -e "${YELLOW}Press Ctrl+C to stop${NC}"
echo ""
./llm-chat
