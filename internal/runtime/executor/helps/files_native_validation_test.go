package helps

import (
	"net/http"
	"testing"
)

func TestFileDestinationRejectsCredentialForwarding(t *testing.T) {
	for _, tc := range []struct {
		provider, url string
		valid         bool
	}{
		{"codex", "https://chatgpt.com/backend-api/codex/responses", true},
		{"codex", "https://evil.invalid/backend-api/codex/responses", false},
		{"codex", "https://chatgpt.com/backend-api/codex/responses/compact", false},
		{"claude", "https://api.anthropic.com/v1/messages", true},
		{"claude", "https://api.anthropic.com/v1/messages?beta=true", true},
		{"claude", "https://api.anthropic.com/v1/messages?beta=false", false},
		{"claude", "https://api.anthropic.com/v1/messages?beta=true&beta=true", false},
		{"claude", "https://api.anthropic.com/v1/messages?beta=true&key=secret", false},
		{"claude", "https://api.anthropic.com/v1/messages?beta%3Dtrue", false},
		{"claude", "https://api.anthropic.com/v1/messages?beta=%74rue", false},
		{"claude", "https://api.anthropic.com/v1/messages?beta=true&", false},
		{"claude", "https://api.anthropic.com/v1/files", false},
		{"gemini", "https://generativelanguage.googleapis.com/v1beta/models/gemini:streamGenerateContent?alt=sse", true},
		{"gemini", "https://generativelanguage.googleapis.com/v1beta/models/gemini:countTokens", false},
		{"deepseek", "https://api.deepseek.com/v1/chat/completions", true},
	} {
		req, _ := http.NewRequest("POST", tc.url, nil)
		if err := ValidateFileDestination(req, tc.provider); (err == nil) != tc.valid {
			t.Fatalf("%s: %v", tc.url, err)
		}
	}
}

func TestGeminiFilePlacementRejectsToolResultData(t *testing.T) {
	for _, tc := range []struct {
		body  string
		valid bool
	}{
		{`{"contents":[{"parts":[{"fileData":{"mimeType":"image/png","fileUri":"owned"}}]}]}`, true},
		{`{"contents":[{"parts":[{"functionResponse":{"parts":[{"fileData":{"mimeType":"image/png","fileUri":"owned"}}]}}]}]}`, true},
		{`{"contents":[{"parts":[{"functionResponse":{"response":{"fileData":{"mimeType":"image/png","fileUri":"owned"}}}}]}]}`, false},
		{`{"systemInstruction":{"parts":[{"fileData":{"mimeType":"image/png","fileUri":"owned"}}]}}`, false},
	} {
		if err := ValidateGeminiFilePlacement([]byte(tc.body)); (err == nil) != tc.valid {
			t.Fatalf("placement %s: %v", tc.body, err)
		}
	}
}
