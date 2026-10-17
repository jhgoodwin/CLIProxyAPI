package helps

import (
	"context"
	"testing"
)

func TestAnthropicRawToolCompletion(t *testing.T) {
	ctx := WithAnthropicPayloadLimits(context.Background(), 100, true)
	for _, reason := range []string{"stop", "tool_calls", "length", "content_filter", "unknown", ""} {
		t.Run(reason, func(t *testing.T) {
			var stream AnthropicToolStream
			if err := stream.Validate(ctx, []byte(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{"}}]}}]}`)); err != nil {
				t.Fatal(err)
			}
			body := []byte(`{"choices":[{"delta":{},"finish_reason":"` + reason + `"}]}`)
			err := stream.Validate(ctx, body)
			if reason == "length" || reason == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("malformed/unsupported completion accepted")
			}
			if reason == "" && stream.Validate(ctx, []byte("[DONE]")) == nil {
				t.Fatal("missing raw finish fabricated success")
			}
		})
	}
	var stream AnthropicToolStream
	for _, body := range []string{`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"a\":"}}]}}]}`, `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]},"finish_reason":"tool_calls"}]}`, `[DONE]`} {
		if err := stream.Validate(ctx, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	for _, invalid := range []string{`{`, `{"usage":{"prompt_tokens":-1}}`, `{"usage":{"completion_tokens":1.5}}`, `{"usage":{"prompt_tokens":9007199254740992}}`, `{"usage":{"prompt_tokens_details":{"cached_tokens":-1}}}`} {
		var invalidStream AnthropicToolStream
		if invalidStream.Validate(ctx, []byte(invalid)) == nil {
			t.Fatalf("invalid raw frame accepted: %s", invalid)
		}
	}
	var ordinary AnthropicToolStream
	if err := ordinary.Validate(context.Background(), []byte("[DONE]")); err != nil {
		t.Fatal("ordinary stream changed", err)
	}
}
