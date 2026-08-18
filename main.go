package main

import (
	"bufio"
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

// ModelConfig represents a model entry in models.json
type ModelConfig struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Path    string `json:"path"`
	Port    int    `json:"port"`
	CtxSize int    `json:"ctx_size"`
}

// ModelStatus tracks the runtime state of a model
type ModelStatus struct {
	Config  ModelConfig `json:"config"`
	State   string      `json:"state"` // unloaded, loading, ready, error
	Error   string      `json:"error,omitempty"`
	Started time.Time   `json:"started,omitempty"`
}

// ChatMessage represents a message in the conversation
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest is the incoming chat API request
type ChatRequest struct {
	ModelID  string        `json:"model_id"`
	Messages []ChatMessage `json:"messages"`
}

// Server manages model processes and serves the API
type Server struct {
	models       map[string]*ModelStatus
	mu           sync.RWMutex
	cmd          *exec.Cmd
	activeModel  string
	shutdownCh   chan struct{}
}

var (
	llamaServerBin string
)

func main() {
	// Find llama-server binary
	llamaServerBin = findLlamaServer()

	// Load model config
	models, err := loadModels("models.json")
	if err != nil {
		log.Fatalf("Failed to load models.json: %v", err)
	}

	srv := &Server{
		models:     make(map[string]*ModelStatus),
		shutdownCh: make(chan struct{}),
	}

	for _, m := range models {
		srv.models[m.ID] = &ModelStatus{
			Config: m,
			State:  "unloaded",
		}
	}

	// Setup signal handler for graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("Shutting down...")
		srv.cleanup()
		os.Exit(0)
	}()

	// Setup routes
	mux := http.NewServeMux()
	mux.HandleFunc("/api/models", srv.handleModels)
	mux.HandleFunc("/api/status", srv.handleStatus)
	mux.HandleFunc("/api/load", srv.handleLoad)
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

func findLlamaServer() string {
	// Check common locations
	candidates := []string{
		"llama-server",
		"/usr/local/bin/llama-server",
		filepath.Join(os.Getenv("HOME"), "llama.cpp/build/bin/llama-server"),
		filepath.Join(os.Getenv("HOME"), "llama.cpp/llama-server"),
		"./llama-server",
	}
	for _, c := range candidates {
		if _, err := exec.LookPath(c); err == nil {
			return c
		}
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	log.Println("WARNING: llama-server not found in common locations. Set PATH or place it in the current directory.")
	return "llama-server"
}

func loadModels(path string) ([]ModelConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var models []ModelConfig
	if err := json.Unmarshal(data, &models); err != nil {
		return nil, err
	}
	// Validate paths
	for _, m := range models {
		if _, err := os.Stat(m.Path); err != nil {
			log.Printf("WARNING: Model file not found: %s (%s)", m.ID, m.Path)
		}
	}
	return models, nil
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []ModelStatus
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

	status := map[string]interface{}{
		"active_model": s.activeModel,
		"models":       s.models,
	}
	writeJSON(w, status)
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

	s.mu.Lock()
	ms, exists := s.models[req.ModelID]
	if !exists {
		s.mu.Unlock()
		http.Error(w, "Model not found", http.StatusNotFound)
		return
	}
	s.mu.Unlock()

	// If already loading this model, just return
	if ms.State == "loading" || (ms.State == "ready" && s.activeModel == req.ModelID) {
		writeJSON(w, ms)
		return
	}

	// Kill any existing model
	s.cleanup()

	// Start loading new model
	go s.loadModel(ms)
	writeJSON(w, map[string]string{"status": "loading", "model": req.ModelID})
}

func (s *Server) loadModel(ms *ModelStatus) {
	s.mu.Lock()
	ms.State = "loading"
	ms.Error = ""
	ms.Started = time.Now()
	s.activeModel = ms.Config.ID
	s.mu.Unlock()

	log.Printf("Loading model %s from %s on port %d", ms.Config.ID, ms.Config.Path, ms.Config.Port)

	// Verify model file exists
	if _, err := os.Stat(ms.Config.Path); err != nil {
		s.mu.Lock()
		ms.State = "error"
		ms.Error = fmt.Sprintf("Model file not found: %s", ms.Config.Path)
		s.mu.Unlock()
		log.Printf("Error: %s", ms.Error)
		return
	}

	// Build command
	args := []string{
		"-m", ms.Config.Path,
		"--port", fmt.Sprintf("%d", ms.Config.Port),
		"-c", fmt.Sprintf("%d", ms.Config.CtxSize),
		"--host", "127.0.0.1",
	}
	
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, llamaServerBin, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	s.mu.Lock()
	s.cmd = cmd
	// Store cancel func for cleanup
	s.shutdownCh = make(chan struct{})
	go func() {
		<-s.shutdownCh
		cancel()
	}()
	s.mu.Unlock()

	if err := cmd.Start(); err != nil {
		s.mu.Lock()
		ms.State = "error"
		ms.Error = fmt.Sprintf("Failed to start llama-server: %v", err)
		s.mu.Unlock()
		log.Printf("Error starting llama-server: %v", err)
		return
	}

	// Health check loop
	healthURL := fmt.Sprintf("http://127.0.0.1:%d/health", ms.Config.Port)
	ready := false
	timeout := time.After(3 * time.Minute)
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for !ready {
		select {
		case <-timeout:
			s.mu.Lock()
			ms.State = "error"
			ms.Error = "Model loading timed out (3 minutes)"
			s.mu.Unlock()
			s.cleanup()
			log.Printf("Model %s loading timed out", ms.Config.ID)
			return
		case <-ticker.C:
			resp, err := http.Get(healthURL)
			if err == nil {
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				status := string(body)
				if strings.Contains(status, "ok") || resp.StatusCode == 200 {
					ready = true
				}
			}
		}
	}

	s.mu.Lock()
	ms.State = "ready"
	log.Printf("Model %s is ready! (took %v)", ms.Config.ID, time.Since(ms.Started))
	s.mu.Unlock()
}

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
		http.Error(w, fmt.Sprintf(`{"error":"Model not loaded. Current state: %s"}`, ms.State), http.StatusServiceUnavailable)
		return
	}

	// Forward to llama-server's OpenAI-compatible endpoint
	chatURL := fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", ms.Config.Port)

	payload := map[string]interface{}{
		"model":    ms.Config.ID,
		"messages": req.Messages,
		"stream":   true,
	}
	payloadBytes, _ := json.Marshal(payload)

	// Create the proxied request
	proxyReq, err := http.NewRequest("POST", chatURL, strings.NewReader(string(payloadBytes)))
	if err != nil {
		http.Error(w, `{"error":"Failed to create request"}`, http.StatusInternalServerError)
		return
	}
	proxyReq.Header.Set("Content-Type", "application/json")

	// Stream the response
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(proxyReq)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"Failed to connect to model server: %v"}`, err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		http.Error(w, string(body), resp.StatusCode)
		return
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	// Stream tokens from llama-server to client
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		// SSE format: "data: {...}\n\n"
		if strings.HasPrefix(line, "data: ") {
			fmt.Fprintf(w, "%s\n\n", line)
			flusher.Flush()
		}
	}
}

func (s *Server) cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cmd != nil && s.cmd.Process != nil {
		log.Printf("Killing previous llama-server (PID: %d)", s.cmd.Process.Pid)
		s.cmd.Process.Signal(syscall.SIGTERM)
		// Give it a moment to clean up
		done := make(chan error, 1)
		go func() {
			done <- s.cmd.Wait()
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			s.cmd.Process.Kill()
		}
		s.cmd = nil
	}

	// Update all models to unloaded
	for _, ms := range s.models {
		if ms.State != "unloaded" {
			ms.State = "unloaded"
			ms.Error = ""
		}
	}
	s.activeModel = ""
	
	// Signal shutdown channel
	select {
	case s.shutdownCh <- struct{}{}:
	default:
	}
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
