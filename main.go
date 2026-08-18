package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// SamplingConfig holds per-model sampling parameters sent to llama-server.
type SamplingConfig struct {
	Temperature   *float64 `json:"temperature,omitempty"`
	TopP          *float64 `json:"top_p,omitempty"`
	TopK          *int     `json:"top_k,omitempty"`
	RepeatPenalty *float64 `json:"repeat_penalty,omitempty"`
	MinP          *float64 `json:"min_p,omitempty"`
	MaxTokens     *int     `json:"max_tokens,omitempty"`
}

// ModelConfig is one entry from models.json — the full model registry.
type ModelConfig struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Path         string          `json:"path"`
	Port         int             `json:"port"`
	CtxSize      int             `json:"ctx_size"`
	NGL          int             `json:"ngl"`
	SystemPrompt string          `json:"system_prompt,omitempty"`
	Sampling     *SamplingConfig `json:"sampling,omitempty"`
	ExtraArgs    []string        `json:"extra_args,omitempty"`
}

// ModelStatus is the runtime state exposed via /api/status.
type ModelStatus struct {
	Config  ModelConfig `json:"config"`
	State   string      `json:"state"` // unloaded | loading | ready | error
	Error   string      `json:"error,omitempty"`
	Started time.Time   `json:"started,omitempty"`
}

// ChatMessage is a single turn in the conversation.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest is what the frontend POSTs to /api/chat.
type ChatRequest struct {
	ModelID  string        `json:"model_id"`
	Messages []ChatMessage `json:"messages"`
}

// Server holds all state: model registry, the running llama-server process,
// and the load-generation counter that prevents stale goroutines from
// corrupting state after a model switch.
type Server struct {
	models      map[string]*ModelStatus
	mu          sync.RWMutex
	cmd         *exec.Cmd
	cancelFn    context.CancelFunc // cancels the running llama-server context
	loadCancel  context.CancelFunc // cancels an in-progress load goroutine
	activeModel string
	loadGen     uint64       // incremented on every load; stale loads self-abort
	stderrBuf   *bytes.Buffer // captures llama-server stderr for error surfacing
}

var llamaServerBin string

func main() {
	llamaServerBin = findLlamaServer()

	models, err := loadModels("models.json")
	if err != nil {
		log.Fatalf("Failed to load models.json: %v", err)
	}

	srv := &Server{
		models: make(map[string]*ModelStatus),
	}
	for _, m := range models {
		srv.models[m.ID] = &ModelStatus{Config: m, State: "unloaded"}
	}

	// Graceful shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("Shutting down...")
		srv.cleanup()
		os.Exit(0)
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/models", srv.handleModels)
	mux.HandleFunc("/api/status", srv.handleStatus)
	mux.HandleFunc("/api/load", srv.handleLoad)
	mux.HandleFunc("/api/unload", srv.handleUnload)
	mux.HandleFunc("/api/chat", srv.handleChat)
	mux.Handle("/", http.FileServer(http.Dir("static")))

	addr := ":3000"
	log.Printf("Starting server on %s", addr)
	log.Printf("llama-server binary: %s", llamaServerBin)
	log.Printf("Loaded %d model configs", len(models))

	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

// findLlamaServer checks common install paths.
func findLlamaServer() string {
	candidates := []string{
		"llama-server",
		"/usr/local/bin/llama-server",
		filepath.Join(os.Getenv("HOME"), "llama.cpp/build/bin/llama-server"),
		filepath.Join(os.Getenv("HOME"), "llama.cpp/llama-server"),
		"./llama-server",
	}
	for _, c := range candidates {
		if p, err := exec.LookPath(c); err == nil {
			return p
		}
	}
	log.Println("WARNING: llama-server not found in common locations")
	return "llama-server"
}

// loadModels reads and validates models.json.
func loadModels(path string) ([]ModelConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var models []ModelConfig
	if err := json.Unmarshal(data, &models); err != nil {
		return nil, err
	}
	for _, m := range models {
		if _, err := os.Stat(m.Path); err != nil {
			log.Printf("WARNING: Model file not found: %s (%s)", m.ID, m.Path)
		}
	}
	return models, nil
}

// ── HTTP handlers ────────────────────────────────────────────────────────────

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]ModelStatus, 0, len(s.models))
	for _, ms := range s.models {
		result = append(result, *ms)
	}
	writeJSON(w, result)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	writeJSON(w, map[string]interface{}{
		"active_model": s.activeModel,
		"models":       s.models,
	})
}

func (s *Server) handleLoad(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		ModelID string `json:"model_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	s.mu.RLock()
	ms, exists := s.models[req.ModelID]
	s.mu.RUnlock()
	if !exists {
		http.Error(w, "Model not found", http.StatusNotFound)
		return
	}

	// Already loading or already active → no-op.
	if ms.State == "loading" || (ms.State == "ready" && s.activeModel == req.ModelID) {
		writeJSON(w, ms)
		return
	}

	// Kill whatever is currently running.
	s.cleanup()

	// Start the new load (generation counter prevents stale updates).
	go s.loadModel(ms)
	writeJSON(w, map[string]string{"status": "loading", "model": req.ModelID})
}

func (s *Server) handleUnload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.cleanup()
	writeJSON(w, map[string]string{"status": "unloaded"})
}

// ── Model loading ────────────────────────────────────────────────────────────

func (s *Server) loadModel(ms *ModelStatus) {
	// Bump generation so any still-running previous load self-aborts.
	s.mu.Lock()
	s.loadGen++
	myGen := s.loadGen
	ms.State = "loading"
	ms.Error = ""
	ms.Started = time.Now()
	s.activeModel = ms.Config.ID

	// Create a cancellable context. This context is cancelled ONLY by
	// cleanup() when the user switches models — NOT when loadModel returns.
	ctx, cancel := context.WithCancel(context.Background())
	s.loadCancel = cancel
	s.mu.Unlock()

	log.Printf("Loading model %s from %s on port %d", ms.Config.ID, ms.Config.Path, ms.Config.Port)

	// Pre-flight: verify the GGUF file exists.
	if _, err := os.Stat(ms.Config.Path); err != nil {
		s.setStateIfCurrentGen(ms, myGen, "error",
			fmt.Sprintf("Model file not found: %s", ms.Config.Path))
		return
	}

	// Build llama-server command line from the full registry entry.
	args := []string{
		"-m", ms.Config.Path,
		"--port", fmt.Sprintf("%d", ms.Config.Port),
		"-c", fmt.Sprintf("%d", ms.Config.CtxSize),
		"--host", "127.0.0.1",
	}
	if ms.Config.NGL > 0 {
		args = append(args, "-ngl", fmt.Sprintf("%d", ms.Config.NGL))
	}
	if len(ms.Config.ExtraArgs) > 0 {
		args = append(args, ms.Config.ExtraArgs...)
	}

	cmd := exec.CommandContext(ctx, llamaServerBin, args...)
	cmd.Stdout = os.Stdout

	// Capture stderr so we can surface real errors to the UI.
	stderrBuf := &bytes.Buffer{}
	cmd.Stderr = io.MultiWriter(os.Stderr, stderrBuf)

	s.mu.Lock()
	s.cmd = cmd
	s.cancelFn = cancel
	s.stderrBuf = stderrBuf
	s.mu.Unlock()

	if err := cmd.Start(); err != nil {
		s.setStateIfCurrentGen(ms, myGen, "error",
			fmt.Sprintf("Failed to start llama-server: %v", err))
		return
	}

	// Poll /health until ready, timed out, cancelled, or process died.
	healthURL := fmt.Sprintf("http://127.0.0.1:%d/health", ms.Config.Port)
	timeout := time.After(3 * time.Minute)
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Cancelled by cleanup() or a newer load — exit silently.
			return

		case <-timeout:
			errDetail := tailLines(stderrBuf.String(), 5)
			s.setStateIfCurrentGen(ms, myGen, "error",
				fmt.Sprintf("Model loading timed out (3 min). Last stderr: %s", errDetail))
			s.cleanup()
			return

		case <-ticker.C:
			// Check if the process exited early (e.g. bad model file).
			if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
				errDetail := tailLines(stderrBuf.String(), 10)
				s.setStateIfCurrentGen(ms, myGen, "error",
					fmt.Sprintf("llama-server exited prematurely: %s", errDetail))
				s.cleanup()
				return
			}

			resp, err := http.Get(healthURL)
			if err == nil {
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if strings.Contains(string(body), "ok") || resp.StatusCode == 200 {
					s.mu.Lock()
					if s.loadGen == myGen {
						ms.State = "ready"
					}
					s.mu.Unlock()
					log.Printf("Model %s is ready! (took %v)",
						ms.Config.ID, time.Since(ms.Started))
					return
				}
			}
		}
	}
}

// setStateIfCurrentGen only writes to the model status if this goroutine
// still owns the current load generation, preventing stale updates.
func (s *Server) setStateIfCurrentGen(ms *ModelStatus, gen uint64, state, errMsg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadGen != gen {
		return // a newer load took over; don't touch state
	}
	ms.State = state
	ms.Error = errMsg
	if errMsg != "" {
		log.Printf("Model %s error: %s", ms.Config.ID, errMsg)
	}
}

// tailLines returns the last n non-empty lines from s, joined with " | ".
func tailLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	var out []string
	for i := len(lines) - 1; i >= 0 && len(out) < n; i-- {
		l := strings.TrimSpace(lines[i])
		if l != "" {
			out = append(out, l)
		}
	}
	// Reverse back to chronological order.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	if len(out) == 0 {
		return "(no stderr output captured)"
	}
	return strings.Join(out, " | ")
}

// ── Chat proxy ───────────────────────────────────────────────────────────────

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	s.mu.RLock()
	ms, exists := s.models[req.ModelID]
	activeModel := s.activeModel
	s.mu.RUnlock()

	if !exists {
		http.Error(w, `{"error":"Model not found"}`, http.StatusNotFound)
		return
	}
	if ms.State != "ready" || activeModel != req.ModelID {
		http.Error(w,
			fmt.Sprintf(`{"error":"Model not loaded. Current state: %s"}`, ms.State),
			http.StatusServiceUnavailable)
		return
	}

	// ── Inject system prompt if the model has one and the client didn't
	//    send one already.
	messages := make([]ChatMessage, 0, len(req.Messages)+1)
	hasSystem := false
	for _, m := range req.Messages {
		if m.Role == "system" {
			hasSystem = true
		}
		messages = append(messages, m)
	}
	if !hasSystem && ms.Config.SystemPrompt != "" {
		messages = append([]ChatMessage{
			{Role: "system", Content: ms.Config.SystemPrompt},
		}, messages...)
	}

	// ── Build the payload with per-model sampling parameters.
	payload := map[string]interface{}{
		"model":    ms.Config.ID,
		"messages": messages,
		"stream":   true,
	}
	if samp := ms.Config.Sampling; samp != nil {
		if v := samp.Temperature; v != nil {
			payload["temperature"] = *v
		}
		if v := samp.TopP; v != nil {
			payload["top_p"] = *v
		}
		if v := samp.TopK; v != nil {
			payload["top_k"] = *v
		}
		if v := samp.RepeatPenalty; v != nil {
			payload["repeat_penalty"] = *v
		}
		if v := samp.MinP; v != nil {
			payload["min_p"] = *v
		}
		if v := samp.MaxTokens; v != nil {
			payload["max_tokens"] = *v
		}
	}

	payloadBytes, _ := json.Marshal(payload)
	chatURL := fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", ms.Config.Port)

	proxyReq, err := http.NewRequest("POST", chatURL, bytes.NewReader(payloadBytes))
	if err != nil {
		http.Error(w, `{"error":"Failed to create proxy request"}`, http.StatusInternalServerError)
		return
	}
	proxyReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(proxyReq)
	if err != nil {
		http.Error(w,
			fmt.Sprintf(`{"error":"Failed to connect to model server: %v"}`, err),
			http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		http.Error(w, string(body), resp.StatusCode)
		return
	}

	// Stream SSE tokens from llama-server straight through to the browser.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "data: ") {
			fmt.Fprintf(w, "%s\n\n", line)
			flusher.Flush()
		}
	}
}

// ── Process cleanup ──────────────────────────────────────────────────────────

func (s *Server) cleanup() {
	s.mu.Lock()

	cmd := s.cmd
	cancelFn := s.cancelFn
	loadCancel := s.loadCancel

	s.cmd = nil
	s.cancelFn = nil
	s.loadCancel = nil
	s.stderrBuf = nil
	s.activeModel = ""
	for _, ms := range s.models {
		if ms.State != "unloaded" {
			ms.State = "unloaded"
			ms.Error = ""
		}
	}

	s.mu.Unlock()

	// Cancel any in-progress load goroutine first.
	if loadCancel != nil {
		loadCancel()
	}
	// Cancel the llama-server context (kills the subprocess tree).
	if cancelFn != nil {
		cancelFn()
	}

	// SIGTERM with a 5 s grace period before SIGKILL.
	if cmd != nil && cmd.Process != nil {
		log.Printf("Killing previous llama-server (PID: %d)", cmd.Process.Pid)
		cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			cmd.Process.Kill()
		}
	}
}

// ── Helpers ──────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
