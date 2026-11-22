package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

func (a *API) aiChat(w http.ResponseWriter, r *http.Request) {
	a.proxyAI(w, r, false)
}

func (a *API) aiWritingAssist(w http.ResponseWriter, r *http.Request) {
	a.proxyAI(w, r, true)
}

func (a *API) proxyAI(w http.ResponseWriter, r *http.Request, writingAssist bool) {
	if a.cfg.anthropicKey == "" {
		writeError(w, http.StatusServiceUnavailable, "ai_unavailable", "AI service is not configured")
		return
	}
	input, ok := readObject(w, r)
	if !ok {
		return
	}
	messages := make([]map[string]string, 0, 20)
	system := ""
	if writingAssist {
		prompt, _ := input["prompt"].(string)
		contextText, _ := input["context"].(string)
		if strings.TrimSpace(prompt) == "" || len(prompt) > 12000 || len(contextText) > 20000 {
			writeError(w, http.StatusBadRequest, "invalid_ai_request", "prompt and context are outside the allowed limits")
			return
		}
		system = "You are a creative writing assistant. Generate engaging content based on the user's prompt."
		messages = append(messages, map[string]string{"role": "user", "content": "Context: " + defaultText(contextText, "No context provided") + "\n\nPrompt: " + prompt})
	} else {
		message, _ := input["message"].(string)
		history, _ := input["history"].([]any)
		if len(history) > 19 || strings.TrimSpace(message) == "" || len(message) > 12000 {
			writeError(w, http.StatusBadRequest, "invalid_ai_request", "message is required and history may contain at most 19 messages")
			return
		}
		for _, value := range history {
			item, valid := value.(map[string]any)
			if !valid {
				writeError(w, http.StatusBadRequest, "invalid_ai_request", "Each history item must be an object")
				return
			}
			role, roleOK := item["role"].(string)
			content, contentOK := item["content"].(string)
			if !roleOK || !contentOK || (role != "user" && role != "assistant") || len(content) > 12000 {
				writeError(w, http.StatusBadRequest, "invalid_ai_request", "Invalid chat history item")
				return
			}
			messages = append(messages, map[string]string{"role": role, "content": content})
		}
		messages = append(messages, map[string]string{"role": "user", "content": message})
		rows, err := a.db.Query(r.Context(), `
			SELECT data FROM records WHERE owner_id=$1 AND collection='books' AND deleted_at IS NULL LIMIT 100`,
			userIDFrom(r))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not load library context")
			return
		}
		descriptions := []string{}
		for rows.Next() {
			var raw []byte
			if rows.Scan(&raw) == nil {
				doc := map[string]any{}
				if json.Unmarshal(raw, &doc) == nil {
					descriptions = append(descriptions, fmt.Sprintf("%s by %s (%s, %s)",
						stringField(doc, "title"), stringField(doc, "author"), stringField(doc, "category"), stringField(doc, "status")))
				}
			}
		}
		rows.Close()
		system = "You are a knowledgeable literary assistant helping a reader with their personal library. " +
			"The reader's books: " + strings.Join(descriptions, "; ") +
			". You can discuss literature, themes, and recommendations."
	}
	model := defaultText(strings.TrimSpace(os.Getenv("ANTHROPIC_MODEL")), "claude-3-sonnet-20240229")
	requestData := map[string]any{"model": model, "max_tokens": 1000, "messages": messages}
	if writingAssist {
		requestData["max_tokens"] = 500
	}
	if system != "" {
		requestData["system"] = system
	}
	body, _ := json.Marshal(requestData)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, "https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not create AI request")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", a.cfg.anthropicKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "ai_unavailable", "AI provider could not be reached")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		writeError(w, http.StatusBadGateway, "ai_error", "AI provider returned an error")
		return
	}
	var providerResponse struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&providerResponse); err != nil {
		writeError(w, http.StatusBadGateway, "ai_error", "AI provider returned an invalid response")
		return
	}
	var text strings.Builder
	for _, block := range providerResponse.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	if strings.TrimSpace(text.String()) == "" {
		writeError(w, http.StatusBadGateway, "ai_error", "AI provider returned no text")
		return
	}
	if writingAssist {
		writeJSON(w, http.StatusOK, map[string]string{"content": text.String()})
	} else {
		writeJSON(w, http.StatusOK, map[string]string{"response": text.String()})
	}
}

func defaultText(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
