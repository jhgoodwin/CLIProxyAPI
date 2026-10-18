// Package filesoverride preserves attempt-owned hosted images around original request converters.
package filesoverride

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	claudechat "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/claude/openai/chat-completions"
	clauderesp "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/claude/openai/responses"
	geminiclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/claude"
	gemininative "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/gemini"
	geminichat "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/openai/chat-completions"
	geminiresp "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/openai/responses"
	openaiclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/openai/claude"
	openainative "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/openai/openai/chat-completions"
	openairesp "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/openai/openai/responses"
	sdk "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const Marker = "__cpa_hosted_files"
const sentinel = "https://cpa-files.invalid/hosted-image/"

type Ref struct {
	URI      string `json:"uri"`
	MIMEType string `json:"mime_type"`
}
type metadata struct {
	Provider string         `json:"provider"`
	Refs     map[string]Ref `json:"refs"`
}
type converter func(string, []byte, bool) ([]byte, error)

var installOnce sync.Once

// Install registers request-only wrappers after builtin converters initialize.
// Response translators and unmarked request behavior remain unchanged.
func Install() {
	installOnce.Do(func() {
		pairs := []struct {
			from, to string
			fn       converter
			// ctxFn, when set, replaces fn so request-scoped state (e.g. the
			// pinned upstream.claude.tool-search value) reaches the converter.
			ctxFn func(context.Context, string, []byte, bool) ([]byte, error)
		}{
			{from: "openai-response", to: "openai-response", fn: claudeIdentity},
			{from: "claude", to: "claude", fn: claudeIdentity},
			{from: "gemini", to: "gemini", fn: gemininative.ConvertGeminiRequestToGemini},
			{from: "openai", to: "openai", fn: openainative.ConvertOpenAIRequestToOpenAI},
			{from: "openai", to: "claude", fn: claudechat.ConvertOpenAIRequestToClaude},
			{from: "openai-response", to: "claude", ctxFn: convertResponsesToClaudeContext},
			{from: "openai", to: "gemini", fn: geminichat.ConvertOpenAIRequestToGemini},
			{from: "openai-response", to: "gemini", fn: geminiresp.ConvertOpenAIResponsesRequestToGemini},
			{from: "claude", to: "gemini", fn: geminiclaude.ConvertClaudeRequestToGemini},
			{from: "claude", to: "openai", fn: openaiclaude.ConvertClaudeRequestToOpenAI},
			{from: "openai-response", to: "openai", fn: openairesp.ConvertOpenAIResponsesRequestToOpenAIChatCompletions},
		}
		for _, pair := range pairs {
			p := pair
			sdk.RegisterRequestEnvelope(sdk.FromString(p.from), sdk.FromString(p.to), func(ctx context.Context, r sdk.RequestEnvelope) sdk.RequestEnvelope {
				fn := p.fn
				if p.ctxFn != nil {
					fn = func(model string, body []byte, stream bool) ([]byte, error) { return p.ctxFn(ctx, model, body, stream) }
				}
				r.Body, r.Err = ConvertWithCompat(p.from, p.to, r.Model, r.Body, r.Stream, fn)
				return r
			})
		}
	})
}

func identity(_ string, body []byte, _ bool) ([]byte, error) { return body, nil }

// convertResponsesToClaudeContext matches the builtin envelope converter, which
// reads the request-pinned tool-search value from ctx.
func convertResponsesToClaudeContext(ctx context.Context, model string, body []byte, stream bool) ([]byte, error) {
	r := clauderesp.ConvertOpenAIResponsesRequestEnvelopeToClaude(ctx, sdk.RequestEnvelope{Model: model, Body: body, Stream: stream})
	return r.Body, r.Err
}

// claudeIdentity retains the registry's original missing-route model normalization.
func claudeIdentity(model string, body []byte, _ bool) ([]byte, error) {
	if model != "" && gjson.GetBytes(body, "model").String() != model {
		if updated, err := sjson.SetBytes(body, "model", model); err == nil {
			return updated, nil
		}
	}
	return body, nil
}

// RestoreNative strips attempt metadata and projects hosted references for a native-format request.
// The decorator must only call this for its locally owned upload attempt.
func RestoreNative(format string, body []byte) ([]byte, error) {
	return ConvertWithCompat(format, format, "", body, false, identity)
}

// ConvertWithCompat surrounds the selected original converter, including its compatibility variant.
// Only locally marked bodies are projected; errors return no sendable body.
func ConvertWithCompat(from, to, model string, body []byte, stream bool, original func(string, []byte, bool) ([]byte, error)) ([]byte, error) {
	if !bytes.Contains(body, []byte(`"`+Marker+`"`)) {
		return original(model, body, stream)
	}
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, errors.New("invalid hosted image request")
	}
	raw, ok := root[Marker]
	if !ok {
		return original(model, body, stream)
	}
	encoded, _ := json.Marshal(raw)
	var meta metadata
	if err := json.Unmarshal(encoded, &meta); err != nil {
		return nil, errors.New("invalid hosted image metadata")
	}
	switch meta.Provider {
	case "openai":
		if from != "openai-response" {
			return nil, errors.New("hosted OpenAI Files require the OpenAI Responses source format")
		}
		if to != "openai-response" {
			return nil, errors.New("hosted image provider mismatch")
		}
	case "claude":
		if to != "claude" {
			return nil, errors.New("hosted image provider mismatch")
		}
	case "gemini":
		if to != "gemini" {
			return nil, errors.New("hosted image provider mismatch")
		}
	case "deepseek", "nvidia":
		if to != "openai" {
			return nil, errors.New("hosted image provider mismatch")
		}
	default:
		return nil, errors.New("invalid hosted image provider")
	}
	delete(root, Marker)
	if meta.Refs == nil {
		meta.Refs = map[string]Ref{}
	}
	state := projection{meta: meta, from: from, tokens: map[string]string{}, seen: map[string]int{}}
	if err := state.prepareConversation(root); err != nil {
		return nil, err
	}
	if len(state.tokens) == 0 {
		return nil, errors.New("hosted images missing from source request")
	}
	clean, err := json.Marshal(root)
	if err != nil {
		return nil, err
	}
	translated, err := original(model, clean, stream)
	if err != nil {
		return nil, err
	}
	var target map[string]any
	if err = json.Unmarshal(translated, &target); err != nil {
		return nil, errors.New("invalid hosted image translation")
	}
	// Only provider-native conversation content is eligible for restoring carriers.
	switch to {
	case "claude", "openai":
		err = state.restoreMessages(target, to)
	case "openai-response":
		err = state.restoreResponses(target)
	case "gemini":
		err = state.restoreGemini(target)
	}
	if err != nil {
		return nil, err
	}
	usedIDs := map[string]bool{}
	for token, id := range state.tokens {
		usedIDs[id] = true
		if state.seen[token] != 1 {
			return nil, errors.New("hosted image lost or duplicated during translation")
		}
	}
	for id := range meta.Refs {
		if !usedIDs[id] {
			return nil, errors.New("hosted reference missing from image content")
		}
	}
	result, err := json.Marshal(target)
	if err != nil {
		return nil, err
	}
	if bytes.Contains(result, []byte(sentinel)) || bytes.Contains(result, []byte(`"`+Marker+`"`)) {
		return nil, errors.New("private hosted image carrier escaped translation")
	}
	return result, nil
}

type projection struct {
	meta   metadata
	from   string
	tokens map[string]string
	seen   map[string]int
}

func (p *projection) prepareConversation(root map[string]any) error {
	if p.from == "gemini" {
		contents, _ := root["contents"].([]any)
		for _, entry := range contents {
			if content, ok := entry.(map[string]any); ok {
				if err := p.prepare(content["parts"]); err != nil {
					return err
				}
			}
		}
		return nil
	}
	key := "messages"
	if p.from == "openai-response" {
		key = "input"
	}
	items, _ := root[key].([]any)
	for _, entry := range items {
		item, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if p.from == "openai-response" {
			typ, _ := item["type"].(string)
			if typ == "function_call_output" || typ == "custom_tool_call_output" {
				if err := p.prepare(item["output"]); err != nil {
					return err
				}
				continue
			}
		}
		if err := p.prepare(item["content"]); err != nil {
			return err
		}
	}
	return nil
}

func (p *projection) prepare(v any) error {
	switch node := v.(type) {
	case []any:
		for _, child := range node {
			if err := p.prepare(child); err != nil {
				return err
			}
		}
	case map[string]any:
		id, _ := node["file_id"].(string)
		typ, _ := node["type"].(string)
		if id == "" && typ == "file" {
			if f, ok := node["file"].(map[string]any); ok {
				id, _ = f["file_id"].(string)
			}
		}
		if id == "" && typ == "image" {
			if f, ok := node["source"].(map[string]any); ok {
				id, _ = f["file_id"].(string)
			}
		}
		if id != "" && (p.meta.Provider == "claude" || p.meta.Provider == "deepseek" || p.meta.Provider == "openai") && (typ == "input_image" || typ == "image" || typ == "file") {
			if _, ok := p.meta.Refs[id]; !ok {
				p.meta.Refs[id] = Ref{MIMEType: "image/png"}
			}
		}
		if ref, ok := p.meta.Refs[id]; ok {
			if typ != "input_image" && typ != "image" && typ != "file" {
				return errors.New("hosted file is not an image content part")
			}
			if !strings.HasPrefix(ref.MIMEType, "image/") {
				return errors.New("invalid hosted image media type")
			}
			if (p.meta.Provider == "gemini" || p.meta.Provider == "nvidia") && (ref.URI == "" || strings.Contains(ref.URI, sentinel)) {
				return errors.New("invalid hosted image URI")
			}
			token := fmt.Sprintf("%s%d", sentinel, len(p.tokens))
			p.tokens[token] = id
			for k := range node {
				delete(node, k)
			}
			if p.from != "gemini" {
				node["type"] = "text"
			}
			if p.from == "openai-response" {
				node["type"] = "input_text"
			}
			node["text"] = token
			return nil
		}
		for _, child := range node {
			if err := p.prepare(child); err != nil {
				return err
			}
		}
	case string:
		if strings.Contains(node, sentinel) {
			return errors.New("reserved hosted image carrier in source")
		}
	}
	return nil
}
func (p *projection) native(token, to string) (map[string]any, bool) {
	id, ok := p.tokens[token]
	if !ok {
		return nil, false
	}
	p.seen[token]++
	switch to {
	case "openai-response":
		return map[string]any{"type": "input_image", "file_id": id}, true
	case "claude":
		return map[string]any{"type": "image", "source": map[string]any{"type": "file", "file_id": id}}, true
	case "gemini":
		ref := p.meta.Refs[id]
		return map[string]any{"fileData": map[string]any{"fileUri": ref.URI, "mimeType": ref.MIMEType}}, true
	default:
		if p.meta.Provider == "nvidia" {
			return map[string]any{"type": "image_url", "image_url": map[string]any{"url": p.meta.Refs[id].URI}}, true
		}
		return map[string]any{"type": "file", "file_id": id}, true
	}
}
func (p *projection) content(v any, to string) any {
	if text, ok := v.(string); ok {
		if part, yes := p.native(text, to); yes {
			return []any{part}
		}
		var decoded []any
		if strings.Contains(text, sentinel) && json.Unmarshal([]byte(text), &decoded) == nil {
			return p.content(decoded, to)
		}
		return v
	}
	if arr, ok := v.([]any); ok {
		for i, entry := range arr {
			if part, ok := entry.(map[string]any); ok {
				if text, _ := part["text"].(string); text != "" {
					if native, yes := p.native(text, to); yes {
						arr[i] = native
						continue
					}
				}
				if part["type"] == "tool_result" {
					part["content"] = p.content(part["content"], to)
				}
			}
		}
		return arr
	}
	return v
}
func (p *projection) restoreResponses(root map[string]any) error {
	items, _ := root["input"].([]any)
	for _, entry := range items {
		item, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := item["type"].(string)
		if typ == "function_call_output" || typ == "custom_tool_call_output" {
			item["output"] = p.content(item["output"], "openai-response")
			continue
		}
		if content, exists := item["content"]; exists {
			item["content"] = p.content(content, "openai-response")
		}
	}
	return nil
}

func (p *projection) restoreMessages(root map[string]any, to string) error {
	msgs, _ := root["messages"].([]any)
	for _, entry := range msgs {
		if msg, ok := entry.(map[string]any); ok {
			msg["content"] = p.content(msg["content"], to)
		}
	}
	return nil
}
func (p *projection) restoreGemini(root map[string]any) error {
	contents, _ := root["contents"].([]any)
	for _, entry := range contents {
		content, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		parts, _ := content["parts"].([]any)
		for i, entry := range parts {
			part, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			if text, _ := part["text"].(string); text != "" {
				if native, yes := p.native(text, "gemini"); yes {
					parts[i] = native
					continue
				}
			}
			if response, ok := part["functionResponse"].(map[string]any); ok {
				var images []any
				response["response"] = p.extractTool(response["response"], &images)
				if len(images) > 0 {
					existing, _ := response["parts"].([]any)
					response["parts"] = append(existing, images...)
				}
			}
		}
		content["parts"] = parts
	}
	return nil
}
func (p *projection) extractTool(v any, images *[]any) any {
	switch node := v.(type) {
	case string:
		if native, ok := p.native(node, "gemini"); ok {
			*images = append(*images, native)
			return ""
		}
		if strings.Contains(node, sentinel) {
			var parsed any
			if json.Unmarshal([]byte(node), &parsed) == nil {
				return p.extractTool(parsed, images)
			}
		}
	case []any:
		out := make([]any, 0, len(node))
		for _, child := range node {
			if obj, ok := child.(map[string]any); ok {
				if text, _ := obj["text"].(string); text != "" {
					if native, yes := p.native(text, "gemini"); yes {
						*images = append(*images, native)
						continue
					}
				}
			}
			out = append(out, p.extractTool(child, images))
		}
		return out
	case map[string]any:
		for key, child := range node {
			node[key] = p.extractTool(child, images)
		}
		return node
	}
	return v
}
