package helps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/tidwall/gjson"
)

// AnthropicToolStream validates raw chat arguments before translation can infer
// truncation from malformed tool JSON. Genuine upstream length stops are allowed.
// Ordinary compatibility traffic retains its existing translation behavior.
type AnthropicToolStream struct {
	args     map[int]string
	finished bool
}

func (s *AnthropicToolStream) Validate(ctx context.Context, body []byte) error {
	if ctx == nil {
		return nil
	}
	if _, ok := ctx.Value(anthropicPayloadKey{}).(anthropicPayloadLimits); !ok {
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(body), []byte("[DONE]")) {
		if !s.finished {
			return fmt.Errorf("upstream ended without finish reason")
		}
		return nil
	}
	if !gjson.ValidBytes(body) {
		return fmt.Errorf("invalid upstream stream JSON")
	}
	var validateUsage func(gjson.Result) error
	validateUsage = func(value gjson.Result) error {
		var errUsage error
		value.ForEach(func(_, field gjson.Result) bool {
			if field.IsObject() {
				errUsage = validateUsage(field)
			} else if field.Type != gjson.Number || field.Float() < 0 || field.Float() > 9007199254740991 || field.Float() != float64(field.Int()) {
				errUsage = fmt.Errorf("invalid upstream usage count")
			}
			return errUsage == nil
		})
		return errUsage
	}
	usage := gjson.GetBytes(body, "usage")
	if usage.Exists() && usage.Type != gjson.Null {
		if !usage.IsObject() {
			return fmt.Errorf("invalid upstream usage")
		}
		if errUsage := validateUsage(usage); errUsage != nil {
			return errUsage
		}
	}
	choice := gjson.GetBytes(body, "choices.0")
	choice.Get("delta.tool_calls").ForEach(func(_, call gjson.Result) bool {
		if s.args == nil {
			s.args = map[int]string{}
		}
		index := int(call.Get("index").Int())
		s.args[index] += call.Get("function.arguments").String()
		return true
	})
	reason := choice.Get("finish_reason").String()
	if reason == "" {
		return nil
	}
	if reason != "stop" && reason != "tool_calls" && reason != "length" {
		return fmt.Errorf("unsupported upstream finish reason")
	}
	s.finished = true
	if reason == "length" {
		return nil
	}
	for _, args := range s.args {
		// No argument fragments represents the default empty input object.
		if args == "" {
			continue
		}
		var input map[string]any
		if json.Unmarshal([]byte(args), &input) != nil || input == nil {
			return fmt.Errorf("invalid completed upstream tool arguments")
		}
	}
	return nil
}
