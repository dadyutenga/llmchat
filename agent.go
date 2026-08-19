package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// AgentRequest is what the frontend POSTs to /api/agent.
type AgentRequest struct {
	ModelID  string        `json:"model_id"`
	Messages []ChatMessage `json:"messages"`
}

const maxAgentIterations = 5

// agentSystemPrompt is the ReAct-style instruction appended to the model's
// own system prompt when running in agent mode.
var agentSystemPrompt = `
You can use tools to answer questions that need current or external information. Available tools:

{{TOOL_LIST}}

To use a tool, respond with EXACTLY this format and stop:
Thought: <your reasoning about what to do next>
Action: <tool name>
Action Input: <the input to the tool>

You will then be given an Observation with the tool's result. Continue reasoning and either call another tool the same way, or, once you have enough information, respond with:
Thought: <your reasoning>
Final Answer: <your complete answer to the user>

Rules:
- Only ever output ONE Thought/Action/Action Input block, then stop and wait.
- If you already know the answer confidently and it does not need current information, skip straight to Final Answer.
- Never invent Observations yourself.`

// buildAgentSystemPrompt returns the full system prompt for agent mode,
// merging the model's own system_prompt with the agent instructions.
func buildAgentSystemPrompt(modelSystemPrompt string, tools map[string]*Tool) string {
	var toolList strings.Builder
	for _, t := range tools {
		toolList.WriteString(fmt.Sprintf("- %s\n", t.Description))
	}

	instructions := strings.ReplaceAll(agentSystemPrompt, "{{TOOL_LIST}}", toolList.String())

	if modelSystemPrompt != "" {
		return modelSystemPrompt + "\n\n" + instructions
	}
	return instructions
}

// handleAgentChat runs a bounded ReAct-style reasoning loop with tool use.
func (s *Server) handleAgentChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req AgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"Invalid request body"}`, http.StatusBadRequest)
		return
	}

	// Validate model.
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

	// Build tools and system prompt.
	tools := BuiltinTools()
	sysPrompt := buildAgentSystemPrompt(ms.Config.SystemPrompt, tools)

	// Build working messages: inject system prompt + user messages.
	workingMessages := make([]ChatMessage, 0, len(req.Messages)+1)
	// Check if there's already a system message from the client.
	hasSystem := false
	for _, m := range req.Messages {
		if m.Role == "system" {
			hasSystem = true
			// Replace client's system prompt with ours (agent mode needs the ReAct instructions).
			workingMessages = append(workingMessages, ChatMessage{Role: "system", Content: sysPrompt})
		} else {
			workingMessages = append(workingMessages, m)
		}
	}
	if !hasSystem {
		workingMessages = append([]ChatMessage{{Role: "system", Content: sysPrompt}}, workingMessages...)
	}

	// Set SSE headers.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, `{"error":"Streaming not supported"}`, http.StatusInternalServerError)
		return
	}

	// SSE helper functions.
	sendEvent := func(event string, data interface{}) {
		jsonData, _ := json.Marshal(data)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, jsonData)
		flusher.Flush()
	}

	sendStepEvent := func(eventType, toolName, content string) {
		sendEvent("step", map[string]string{
			"type":   eventType,
			"tool":   toolName,
			"input":  content,
			"output": content,
		})
	}

	sendToken := func(content string) {
		sendEvent("token", map[string]string{"content": content})
	}

	sendDone := func() {
		fmt.Fprintf(w, "event: done\ndata: {}\n\n")
		flusher.Flush()
	}

	// Context with 2-minute timeout for the whole request.
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()

	// Per-llama-server client with 60s timeout.
	llamaClient := &http.Client{Timeout: 60 * time.Second}

	// Regex for parsing Action / Action Input.
	actionRe := regexp.MustCompile(`(?i)Action\s*:\s*(.+)`)
	actionInputRe := regexp.MustCompile(`(?i)Action\s*Input\s*:\s*(.+)`)
	finalAnswerRe := regexp.MustCompile(`(?i)Final\s*Answer\s*:\s*([\s\S]*)`)

	var finalAnswer string

	for iteration := 0; iteration < maxAgentIterations; iteration++ {
		// Check context cancellation.
		if ctx.Err() != nil {
			sendToken("\n\n_(request timed out)_")
			sendDone()
			return
		}

		// Call llama-server with stream: false for the reasoning step.
		payload := map[string]interface{}{
			"model":      ms.Config.ID,
			"messages":   workingMessages,
			"stream":     false,
			"max_tokens": 512,
		}
		applySamplingConfig(payload, ms.Config.Sampling)

		payloadBytes, _ := json.Marshal(payload)
		chatURL := fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", ms.Config.Port)

		proxyReq, err := http.NewRequestWithContext(ctx, "POST", chatURL, bytes.NewReader(payloadBytes))
		if err != nil {
			sendToken(fmt.Sprintf("\n\n_(error creating request: %v)_", err))
			sendDone()
			return
		}
		proxyReq.Header.Set("Content-Type", "application/json")

		resp, err := llamaClient.Do(proxyReq)
		if err != nil {
			sendToken(fmt.Sprintf("\n\n_(error calling model: %v)_", err))
			sendDone()
			return
		}

		bodyBytes, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			sendToken(fmt.Sprintf("\n\n_(error reading response: %v)_", err))
			sendDone()
			return
		}

		if resp.StatusCode != http.StatusOK {
			sendToken(fmt.Sprintf("\n\n_(model server returned HTTP %d)_", resp.StatusCode))
			sendDone()
			return
		}

		// Parse the non-streaming response.
		var llmResp struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(bodyBytes, &llmResp); err != nil {
			sendToken(fmt.Sprintf("\n\n_(error parsing model response: %v)_", err))
			sendDone()
			return
		}

		if len(llmResp.Choices) == 0 {
			sendToken("\n\n_(model returned empty response)_")
			sendDone()
			return
		}

		completion := llmResp.Choices[0].Message.Content

		// Check for Final Answer.
		if matches := finalAnswerRe.FindStringSubmatch(completion); len(matches) > 1 {
			finalAnswer = strings.TrimSpace(matches[1])
			break
		}

		// Check for Action / Action Input.
		actionMatch := actionRe.FindStringSubmatch(completion)
		actionInputMatch := actionInputRe.FindStringSubmatch(completion)

		if len(actionMatch) > 1 && len(actionInputMatch) > 1 {
			toolName := strings.TrimSpace(actionMatch[1])
			toolInput := strings.TrimSpace(actionInputMatch[1])
			// Strip surrounding quotes if present.
			toolInput = strings.Trim(toolInput, `"'`)

			// Look up tool.
			tool, found := tools[toolName]
			if !found {
				// Tell the model the tool doesn't exist.
				observation := fmt.Sprintf("Observation: Unknown tool '%s'. Available tools: web_search, fetch_url.", toolName)
				workingMessages = append(workingMessages, ChatMessage{Role: "assistant", Content: completion})
				workingMessages = append(workingMessages, ChatMessage{Role: "user", Content: observation})
				continue
			}

			// Emit step event to the browser.
			sendStepEvent("action", toolName, toolInput)

			// Run the tool.
			toolCtx, toolCancel := context.WithTimeout(ctx, 15*time.Second)
			toolOutput, toolErr := tool.Run(toolCtx, toolInput)
			toolCancel()

			if toolErr != nil {
				toolOutput = fmt.Sprintf("Tool error: %v", toolErr)
			}

			// Emit observation event.
			sendStepEvent("observation", toolName, toolOutput)

			// Append the model's action text + observation to working messages.
			workingMessages = append(workingMessages, ChatMessage{Role: "assistant", Content: completion})
			workingMessages = append(workingMessages, ChatMessage{Role: "user", Content: fmt.Sprintf("Observation: %s", toolOutput)})
			continue
		}

		// Model didn't follow the format — treat as Final Answer (best-effort).
		finalAnswer = strings.TrimSpace(completion)
		break
	}

	// If we exhausted iterations without a Final Answer.
	if finalAnswer == "" {
		finalAnswer = "_(stopped after 5 tool calls without a final answer)_"
	}

	// Now stream the final answer token-by-token by re-issuing a streaming request.
	streamFinalAnswer(ctx, llamaClient, ms, workingMessages, finalAnswer, sendToken, sendDone)
}

// streamFinalAnswer re-issues a streaming request to llama-server to stream
// the final answer token-by-token for a better UX.
func streamFinalAnswer(
	ctx context.Context,
	client *http.Client,
	ms *ModelStatus,
	workingMessages []ChatMessage,
	fallbackAnswer string,
	sendToken func(string),
	sendDone func(),
) {
	// Append a prompt to generate the final answer.
	workingMessages = append(workingMessages, ChatMessage{
		Role:    "user",
		Content: "Please now give your Final Answer based on the information above.",
	})

	payload := map[string]interface{}{
		"model":    ms.Config.ID,
		"messages": workingMessages,
		"stream":   true,
	}
	applySamplingConfig(payload, ms.Config.Sampling)

	payloadBytes, _ := json.Marshal(payload)
	chatURL := fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", ms.Config.Port)

	proxyReq, err := http.NewRequestWithContext(ctx, "POST", chatURL, bytes.NewReader(payloadBytes))
	if err != nil {
		// Fall back to sending the answer directly.
		sendToken(fallbackAnswer)
		sendDone()
		return
	}
	proxyReq.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(proxyReq)
	if err != nil {
		sendToken(fallbackAnswer)
		sendDone()
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		sendToken(fallbackAnswer)
		sendDone()
		return
	}

	// Stream SSE tokens.
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			continue
		}

		var parsed struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &parsed); err != nil {
			continue
		}
		if len(parsed.Choices) > 0 && parsed.Choices[0].Delta.Content != "" {
			sendToken(parsed.Choices[0].Delta.Content)
		}
	}

	sendDone()
}

// applySamplingConfig adds per-model sampling parameters to the payload.
func applySamplingConfig(payload map[string]interface{}, samp *SamplingConfig) {
	if samp == nil {
		return
	}
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
