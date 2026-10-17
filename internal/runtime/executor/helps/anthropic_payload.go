package helps

import (
	"context"
	"fmt"

	"github.com/tidwall/gjson"
)

type anthropicPayloadKey struct{}
type anthropicPayloadLimits struct {
	MaxOutput int
	Reasoning bool
}

// WithAnthropicPayloadLimits marks the opt-in DSH surface for read-only final
// validation after user payload rules. Ordinary OpenAI traffic is untouched.
func WithAnthropicPayloadLimits(ctx context.Context, maxOutput int, reasoning bool) context.Context {
	return context.WithValue(ctx, anthropicPayloadKey{}, anthropicPayloadLimits{maxOutput, reasoning})
}
func ValidateAnthropicPayload(ctx context.Context, body []byte) error {
	if ctx == nil {
		return nil
	}
	limits, ok := ctx.Value(anthropicPayloadKey{}).(anthropicPayloadLimits)
	if !ok {
		return nil
	}
	if !gjson.ValidBytes(body) {
		return fmt.Errorf("invalid final Anthropic upstream payload")
	}
	token := gjson.GetBytes(body, "max_completion_tokens")
	other := gjson.GetBytes(body, "max_tokens")
	if token.Exists() && other.Exists() {
		return fmt.Errorf("conflicting final output limits")
	}
	if !token.Exists() {
		token = other
	}
	if !token.Exists() || token.Type != gjson.Number || token.Float() != float64(token.Int()) || token.Int() <= 0 || token.Int() > int64(limits.MaxOutput) {
		return fmt.Errorf("final output limit exceeds configured Anthropic capability")
	}
	effort := gjson.GetBytes(body, "reasoning_effort")
	if effort.Exists() {
		s := effort.String()
		if s != "none" && s != "auto" && s != "low" && s != "high" && s != "max" {
			return fmt.Errorf("unsupported final reasoning effort")
		}
		if !limits.Reasoning && s != "none" {
			return fmt.Errorf("final reasoning enabled for unsupported model")
		}
	}
	return nil
}
