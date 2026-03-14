package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/openblocklabs/obl-compat-proxy/internal/compat"
	"github.com/openblocklabs/obl-compat-proxy/internal/config"
	"github.com/openblocklabs/obl-compat-proxy/internal/normalize"
	"github.com/openblocklabs/obl-compat-proxy/internal/obl"
	"github.com/openblocklabs/obl-compat-proxy/internal/sse"
)

type Server struct {
	cfg    config.Config
	client *obl.Client
	mux    *http.ServeMux
}

func NewServer(cfg config.Config) (*Server, error) {
	server := &Server{
		cfg:    cfg,
		client: obl.NewClient(cfg),
		mux:    http.NewServeMux(),
	}

	server.registerRoutes()
	return server, nil
}

func (s *Server) Handler() http.Handler {
	return s.mux
}

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	s.mux.Handle("GET /v1/models", s.withAuth(s.handleModels))
	s.mux.Handle("POST /v1/chat/completions", s.withAuth(s.handleOpenAIChat))
	s.mux.Handle("POST /v1/messages", s.withAuth(s.handleAnthropicMessages))
}

func (s *Server) withAuth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			if strings.HasPrefix(r.URL.Path, "/v1/messages") {
				writeAnthropicError(w, http.StatusUnauthorized, "authentication_error", "invalid API key")
				return
			}
			writeOpenAIError(w, http.StatusUnauthorized, "authentication_error", "invalid API key", "invalid_api_key")
			return
		}
		next(w, r)
	})
}

func (s *Server) authorized(r *http.Request) bool {
	received := strings.TrimSpace(r.Header.Get("x-api-key"))
	if received == "" {
		auth := strings.TrimSpace(r.Header.Get("Authorization"))
		if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
			received = strings.TrimSpace(auth[7:])
		}
	}
	if received == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(received), []byte(s.cfg.ProxyAPIKey)) == 1
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
	})
}

func (s *Server) handleModels(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, compat.BuildOpenAIModelList(s.cfg.ModelRegistry))
}

func (s *Server) handleOpenAIChat(w http.ResponseWriter, r *http.Request) {
	body, ok := s.readBody(w, r, false)
	if !ok {
		return
	}

	req, err := compat.ParseOpenAIRequest(body, s.cfg.ModelRegistry, s.cfg.ImageDataURLMaxBytes)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", err.Error(), "invalid_request")
		return
	}

	upstreamResp, err := s.client.StreamChat(r.Context(), obl.BuildRequest(req, req.IncludeUsage || !req.Stream))
	if err != nil {
		s.writeOpenAIUpstreamError(w, err)
		return
	}
	defer upstreamResp.Body.Close()

	if req.Stream {
		if err := proxyOpenAIStream(w, upstreamResp, req.Model.ID, req.IncludeUsage); err != nil {
			writeOpenAIError(w, http.StatusBadGateway, "api_error", err.Error(), "upstream_stream_error")
		}
		return
	}

	aggregate, err := collectAggregate(upstreamResp)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, "api_error", err.Error(), "upstream_stream_error")
		return
	}
	writeJSON(w, http.StatusOK, compat.BuildOpenAIResponse(aggregate, req.Model.ID))
}

func (s *Server) handleAnthropicMessages(w http.ResponseWriter, r *http.Request) {
	body, ok := s.readBody(w, r, true)
	if !ok {
		return
	}

	req, err := compat.ParseAnthropicRequest(body, s.cfg.ModelRegistry, s.cfg.ImageDataURLMaxBytes)
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	req.AnthropicBetas = anthropicBetasForRequest(r, req)

	upstreamResp, err := s.client.StreamChat(r.Context(), obl.BuildRequest(req, true))
	if err != nil {
		s.writeAnthropicUpstreamError(w, err)
		return
	}
	defer upstreamResp.Body.Close()

	if req.Stream {
		if err := proxyAnthropicStream(w, upstreamResp, req.Model.ID); err != nil {
			writeAnthropicError(w, http.StatusBadGateway, "api_error", err.Error())
		}
		return
	}

	aggregate, err := collectAggregate(upstreamResp)
	if err != nil {
		writeAnthropicError(w, http.StatusBadGateway, "api_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, compat.BuildAnthropicResponse(aggregate, req.Model.ID))
}

func anthropicBetasForRequest(r *http.Request, req normalize.Request) []string {
	betas := splitHeaderValues(r.Header.Values("anthropic-beta"))
	if req.Thinking.Enabled && len(req.Tools) > 0 && !containsString(betas, "interleaved-thinking-2025-05-14") {
		betas = append(betas, "interleaved-thinking-2025-05-14")
	}
	return betas
}

func (s *Server) readBody(w http.ResponseWriter, r *http.Request, anthropic bool) ([]byte, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.RequestBodyMaxBytes)
	defer r.Body.Close()

	raw, err := io.ReadAll(r.Body)
	if err != nil {
		if anthropic {
			writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", fmt.Sprintf("read body: %v", err))
		} else {
			writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", fmt.Sprintf("read body: %v", err), "invalid_request")
		}
		return nil, false
	}
	return raw, true
}

func proxyOpenAIStream(w http.ResponseWriter, upstreamResp *http.Response, requestedModel string, includeUsage bool) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("streaming is unsupported by the response writer")
	}
	setSSEHeaders(w)

	return sse.ParseStream(upstreamResp.Body, func(event sse.Event) error {
		if event.Data == "" {
			return nil
		}
		if event.Data == "[DONE]" {
			if err := sse.WriteEvent(w, "", "[DONE]"); err != nil {
				return err
			}
			flusher.Flush()
			return nil
		}

		var chunk obl.Chunk
		if err := json.Unmarshal([]byte(event.Data), &chunk); err != nil {
			return fmt.Errorf("decode upstream chunk: %w", err)
		}
		if chunk.Usage != nil && !includeUsage {
			return nil
		}
		payload, err := compat.RewriteOpenAIChunk([]byte(event.Data), requestedModel)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		if err := sse.WriteEvent(w, "", string(encoded)); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	})
}

func proxyAnthropicStream(w http.ResponseWriter, upstreamResp *http.Response, requestedModel string) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("streaming is unsupported by the response writer")
	}
	setSSEHeaders(w)

	var (
		aggregate          obl.Aggregate
		started            bool
		nextIndex          int
		active             anthropicStreamBlock
		pending            []anthropicBufferedEvent
		pendingLeadingText string
		sawThinking        bool
	)

	openBlock := func(kind anthropicBlockKind, tool *obl.ChunkToolCallPart) error {
		if active.kind == kind {
			if kind != anthropicBlockTool || active.toolIndex == tool.Index {
				return nil
			}
		}

		if err := closeAnthropicBlock(w, &active); err != nil {
			return err
		}

		payload := map[string]any{
			"type":  "content_block_start",
			"index": nextIndex,
		}
		switch kind {
		case anthropicBlockThinking:
			payload["content_block"] = map[string]any{
				"type":     "thinking",
				"thinking": "",
			}
		case anthropicBlockText:
			payload["content_block"] = map[string]any{
				"type": "text",
				"text": "",
			}
		case anthropicBlockTool:
			payload["content_block"] = map[string]any{
				"type":  "tool_use",
				"id":    tool.ID,
				"name":  tool.Function.Name,
				"input": map[string]any{},
			}
		default:
			return nil
		}
		encoded, _ := json.Marshal(payload)
		if err := writeAnthropicEvent(w, "content_block_start", string(encoded)); err != nil {
			return err
		}

		active = anthropicStreamBlock{
			kind:      kind,
			index:     nextIndex,
			toolIndex: -1,
			started:   true,
		}
		if tool != nil {
			active.toolIndex = tool.Index
		}
		nextIndex++
		return nil
	}

	emitText := func(text string) error {
		if err := openBlock(anthropicBlockText, nil); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{
			"type":  "content_block_delta",
			"index": active.index,
			"delta": map[string]any{
				"type": "text_delta",
				"text": text,
			},
		})
		return writeAnthropicEvent(w, "content_block_delta", string(payload))
	}

	emitTool := func(tool obl.ChunkToolCallPart) error {
		if err := openBlock(anthropicBlockTool, &tool); err != nil {
			return err
		}
		if tool.Function.Arguments == "" {
			return nil
		}
		payload, _ := json.Marshal(map[string]any{
			"type":  "content_block_delta",
			"index": active.index,
			"delta": map[string]any{
				"type":         "input_json_delta",
				"partial_json": tool.Function.Arguments,
			},
		})
		return writeAnthropicEvent(w, "content_block_delta", string(payload))
	}

	flushPending := func() error {
		if pendingLeadingText != "" {
			appendAnthropicBufferedText(&pending, pendingLeadingText)
			pendingLeadingText = ""
		}
		if err := flushAnthropicBufferedEvents(w, &pending, &active, emitText, emitTool, flusher); err != nil {
			return err
		}
		return nil
	}

	err := sse.ParseStream(upstreamResp.Body, func(event sse.Event) error {
		if event.Data == "" {
			return nil
		}
		if event.Data == "[DONE]" {
			if active.kind == anthropicBlockThinking {
				if err := closeAnthropicBlock(w, &active); err != nil {
					return err
				}
			}
			if err := flushPending(); err != nil {
				return err
			}
			if err := closeAnthropicBlock(w, &active); err != nil {
				return err
			}
			payload, _ := json.Marshal(map[string]any{
				"type": "message_delta",
				"delta": map[string]any{
					"stop_reason":   compat.AnthropicStopReason(aggregate.FinishReason),
					"stop_sequence": nil,
				},
				"usage": compat.BuildAnthropicUsage(aggregate.Usage),
			})
			if err := writeAnthropicEvent(w, "message_delta", string(payload)); err != nil {
				return err
			}
			if err := writeAnthropicEvent(w, "message_stop", `{"type":"message_stop"}`); err != nil {
				return err
			}
			flusher.Flush()
			return nil
		}

		var chunk obl.Chunk
		if err := json.Unmarshal([]byte(event.Data), &chunk); err != nil {
			return fmt.Errorf("decode upstream chunk: %w", err)
		}

		if !started {
			aggregate.ID = chunk.ID
			aggregate.Created = chunk.Created
			aggregate.Model = requestedModel
			payload, _ := json.Marshal(compat.BuildAnthropicMessageStart(aggregate, requestedModel))
			if err := writeAnthropicEvent(w, "message_start", string(payload)); err != nil {
				return err
			}
			started = true
		}

		aggregate.Consume(chunk)
		for _, choice := range chunk.Choices {
			reasoningText, reasoningSignature := anthropicReasoningDelta(choice.Delta)
			if reasoningText != "" || reasoningSignature != "" {
				sawThinking = true
				if err := openBlock(anthropicBlockThinking, nil); err != nil {
					return err
				}
				if pendingLeadingText != "" {
					appendAnthropicBufferedText(&pending, pendingLeadingText)
					pendingLeadingText = ""
				}
				if reasoningText != "" {
					payload, _ := json.Marshal(map[string]any{
						"type":  "content_block_delta",
						"index": active.index,
						"delta": map[string]any{
							"type":     "thinking_delta",
							"thinking": reasoningText,
						},
					})
					if err := writeAnthropicEvent(w, "content_block_delta", string(payload)); err != nil {
						return err
					}
				}
				if reasoningSignature != "" {
					payload, _ := json.Marshal(map[string]any{
						"type":  "content_block_delta",
						"index": active.index,
						"delta": map[string]any{
							"type":      "signature_delta",
							"signature": reasoningSignature,
						},
					})
					if err := writeAnthropicEvent(w, "content_block_delta", string(payload)); err != nil {
						return err
					}
					if err := closeAnthropicBlock(w, &active); err != nil {
						return err
					}
					sawThinking = false
					if err := flushPending(); err != nil {
						return err
					}
				}
				continue
			}

			if choice.Delta.Content != "" {
				if active.kind == anthropicBlockThinking || sawThinking {
					if pendingLeadingText != "" {
						appendAnthropicBufferedText(&pending, pendingLeadingText)
						pendingLeadingText = ""
					}
					appendAnthropicBufferedText(&pending, choice.Delta.Content)
					continue
				}
				if strings.TrimSpace(choice.Delta.Content) == "" && active.kind == anthropicBlockNone {
					pendingLeadingText += choice.Delta.Content
					continue
				}
				if pendingLeadingText != "" {
					choice.Delta.Content = pendingLeadingText + choice.Delta.Content
					pendingLeadingText = ""
				}
				if err := emitText(choice.Delta.Content); err != nil {
					return err
				}
			}

			for _, tool := range choice.Delta.ToolCalls {
				if active.kind == anthropicBlockThinking || sawThinking {
					if pendingLeadingText != "" {
						appendAnthropicBufferedText(&pending, pendingLeadingText)
						pendingLeadingText = ""
					}
					pending = append(pending, anthropicBufferedEvent{
						kind: anthropicBufferedTool,
						tool: tool,
					})
					continue
				}
				if err := emitTool(tool); err != nil {
					return err
				}
			}
		}

		flusher.Flush()
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

func collectAggregate(upstreamResp *http.Response) (obl.Aggregate, error) {
	var aggregate obl.Aggregate
	err := sse.ParseStream(upstreamResp.Body, func(event sse.Event) error {
		if event.Data == "" || event.Data == "[DONE]" {
			return nil
		}
		var chunk obl.Chunk
		if err := json.Unmarshal([]byte(event.Data), &chunk); err != nil {
			return fmt.Errorf("decode upstream chunk: %w", err)
		}
		aggregate.Consume(chunk)
		return nil
	})
	return aggregate, err
}

func (s *Server) writeOpenAIUpstreamError(w http.ResponseWriter, err error) {
	if upstreamErr, ok := err.(*obl.UpstreamError); ok {
		writeOpenAIError(w, upstreamErr.StatusCode, defaultString(upstreamErr.Type, "api_error"), upstreamErr.Message, upstreamErr.Code)
		return
	}
	writeOpenAIError(w, http.StatusBadGateway, "api_error", err.Error(), "upstream_error")
}

func (s *Server) writeAnthropicUpstreamError(w http.ResponseWriter, err error) {
	if upstreamErr, ok := err.(*obl.UpstreamError); ok {
		writeAnthropicError(w, upstreamErr.StatusCode, defaultString(upstreamErr.Type, "api_error"), upstreamErr.Message)
		return
	}
	writeAnthropicError(w, http.StatusBadGateway, "api_error", err.Error())
}

func setSSEHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
}

func writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_, _ = w.Write(body)
}

func writeOpenAIError(w http.ResponseWriter, statusCode int, errType string, message string, code string) {
	payload := map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    errType,
			"code":    code,
		},
	}
	writeJSON(w, statusCode, payload)
}

func writeAnthropicError(w http.ResponseWriter, statusCode int, errType string, message string) {
	payload := map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    errType,
			"message": message,
		},
	}
	writeJSON(w, statusCode, payload)
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func splitHeaderValues(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		for _, item := range strings.Split(value, ",") {
			item = strings.TrimSpace(item)
			if item == "" || containsString(out, item) {
				continue
			}
			out = append(out, item)
		}
	}
	return out
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(want)) {
			return true
		}
	}
	return false
}

func writeAnthropicEvent(w http.ResponseWriter, eventType string, payload string) error {
	return sse.WriteEvent(w, eventType, payload)
}

type anthropicBlockKind string

const (
	anthropicBlockNone     anthropicBlockKind = ""
	anthropicBlockThinking anthropicBlockKind = "thinking"
	anthropicBlockText     anthropicBlockKind = "text"
	anthropicBlockTool     anthropicBlockKind = "tool"
	// A tiny delay prevents local clients from receiving all buffered deltas
	// in one lump once the thinking signature arrives.
	anthropicBufferedFlushPace = 10 * time.Millisecond
)

type anthropicStreamBlock struct {
	kind      anthropicBlockKind
	index     int
	toolIndex int
	started   bool
}

type anthropicBufferedEventKind string

const (
	anthropicBufferedText anthropicBufferedEventKind = "text"
	anthropicBufferedTool anthropicBufferedEventKind = "tool"
)

type anthropicBufferedEvent struct {
	kind anthropicBufferedEventKind
	text string
	tool obl.ChunkToolCallPart
}

func closeAnthropicBlock(w http.ResponseWriter, active *anthropicStreamBlock) error {
	if active == nil || !active.started || active.kind == anthropicBlockNone {
		return nil
	}
	payload, _ := json.Marshal(map[string]any{
		"type":  "content_block_stop",
		"index": active.index,
	})
	if err := writeAnthropicEvent(w, "content_block_stop", string(payload)); err != nil {
		return err
	}
	*active = anthropicStreamBlock{kind: anthropicBlockNone, toolIndex: -1}
	return nil
}

func anthropicReasoningDelta(delta obl.ChunkDelta) (string, string) {
	var text string
	if delta.Reasoning != nil {
		text = *delta.Reasoning
	}
	var signature string
	for _, detail := range delta.ReasoningDetails {
		switch detail.Type {
		case "reasoning.text":
			if text == "" && detail.Text != "" {
				text = detail.Text
			}
			if detail.Signature != "" {
				signature = detail.Signature
			}
		case "reasoning.summary":
			if text == "" && detail.Summary != "" {
				text = detail.Summary
			}
		}
	}
	return text, signature
}

func appendAnthropicBufferedText(buffer *[]anthropicBufferedEvent, text string) {
	if text == "" {
		return
	}
	*buffer = append(*buffer, anthropicBufferedEvent{
		kind: anthropicBufferedText,
		text: text,
	})
}

func flushAnthropicBufferedEvents(
	w http.ResponseWriter,
	buffer *[]anthropicBufferedEvent,
	active *anthropicStreamBlock,
	emitText func(string) error,
	emitTool func(obl.ChunkToolCallPart) error,
	flusher http.Flusher,
) error {
	for i, item := range *buffer {
		switch item.kind {
		case anthropicBufferedText:
			if err := emitText(item.text); err != nil {
				return err
			}
		case anthropicBufferedTool:
			if err := emitTool(item.tool); err != nil {
				return err
			}
		}
		flusher.Flush()
		if i < len(*buffer)-1 {
			time.Sleep(anthropicBufferedFlushPace)
		}
	}
	*buffer = nil
	if err := closeAnthropicBlock(w, active); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}
