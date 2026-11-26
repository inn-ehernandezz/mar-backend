package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"mar-backend/internal/platform/inference"
)

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type Tool struct {
	Type     string   `json:"type"`
	Function Function `json:"function"`
}

type Function struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type ModelEndpoint struct {
	URL              string
	Model            string
	APIKey           string
	Client           *http.Client
	GPU              *inference.GPU
	MaxResponseBytes int64
}

type HTTPChat struct{ ModelEndpoint }

func (m HTTPChat) Complete(ctx context.Context, messages []Message, tools []Tool, schema json.RawMessage) (Message, error) {
	request := struct {
		Model          string         `json:"model"`
		Messages       []Message      `json:"messages"`
		Tools          []Tool         `json:"tools,omitempty"`
		ToolChoice     string         `json:"tool_choice,omitempty"`
		MaxTokens      int            `json:"max_tokens"`
		Temperature    float64        `json:"temperature"`
		ResponseFormat map[string]any `json:"response_format,omitempty"`
	}{
		Model: m.Model, Messages: messages, Tools: tools, MaxTokens: 480,
	}
	if len(tools) > 0 {
		request.ToolChoice = "auto"
	} else {
		request.ResponseFormat = map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name": "mar_response", "schema": schema,
			},
		}
	}
	var response struct {
		Choices []struct {
			Message      Message `json:"message"`
			FinishReason string  `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := m.post(ctx, "/v1/chat/completions", request, &response); err != nil {
		return Message{}, err
	}
	if len(response.Choices) != 1 || response.Choices[0].Message.Role != "assistant" {
		return Message{}, errors.New("el servidor devolvió un turno de asistente inválido")
	}
	choice := response.Choices[0]
	if choice.FinishReason == "length" {
		return Message{}, errors.New("el modelo agotó el límite de tokens")
	}
	ids := make(map[string]bool)
	for _, call := range choice.Message.ToolCalls {
		if call.Type != "function" || call.ID == "" || ids[call.ID] {
			return Message{}, errors.New("el modelo devolvió una llamada a herramienta inválida")
		}
		ids[call.ID] = true
	}
	return choice.Message, nil
}

type HTTPReranker struct{ ModelEndpoint }

// BGE Reranker v2 M3, servido por vLLM, devuelve puntuaciones normalizadas.
// Este suelo descarta coincidencias muy débiles antes de enviarlas al modelo.
const minPassageRelevance = 0.05

func (m HTTPReranker) Rank(ctx context.Context, question string, texts []string) ([]int, error) {
	request := struct {
		Model     string   `json:"model"`
		Query     string   `json:"query"`
		Documents []string `json:"documents"`
	}{m.Model, question, texts}
	type ranked struct {
		Index *int     `json:"index"`
		Score *float64 `json:"relevance_score"`
	}
	var response struct {
		Results []ranked `json:"results"`
	}
	if err := m.post(ctx, "/rerank", request, &response); err != nil {
		return nil, err
	}
	if len(response.Results) != len(texts) {
		return nil, errors.New("el reranker devolvió un número incorrecto de resultados")
	}
	seen := make([]bool, len(texts))
	for _, hit := range response.Results {
		if hit.Index == nil || hit.Score == nil || *hit.Index < 0 || *hit.Index >= len(seen) || seen[*hit.Index] {
			return nil, errors.New("el reranker devolvió un índice o una puntuación inválidos")
		}
		seen[*hit.Index] = true
	}
	sort.SliceStable(response.Results, func(i, j int) bool {
		a, b := response.Results[i], response.Results[j]
		if *a.Score == *b.Score {
			return *a.Index < *b.Index
		}
		return *a.Score > *b.Score
	})
	order := make([]int, 0, len(texts))
	for _, hit := range response.Results {
		if *hit.Score < minPassageRelevance {
			break
		}
		order = append(order, *hit.Index)
	}
	return order, nil
}

func (m ModelEndpoint) post(ctx context.Context, path string, input, output any) error {
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(m.URL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if m.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+m.APIKey)
	}
	res, err := m.GPU.Do(m.Client, req, path)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("servidor de inferencia: HTTP %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, m.MaxResponseBytes+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > m.MaxResponseBytes {
		return errors.New("la respuesta del servidor de inferencia supera el límite")
	}
	if err := json.Unmarshal(data, output); err != nil {
		return errors.New("el servidor de inferencia devolvió un JSON inválido")
	}
	return nil
}
