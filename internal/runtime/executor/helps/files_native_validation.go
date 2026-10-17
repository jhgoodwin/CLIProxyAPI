package helps

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// ValidateFileDestination restricts credential restoration to native inference endpoints.
func ValidateFileDestination(req *http.Request, provider string) error {
	if req.Method != http.MethodPost || req.URL == nil || req.URL.Scheme != "https" || req.URL.User != nil || req.URL.Fragment != "" {
		return fmt.Errorf("unsupported hosted file inference destination")
	}
	u := req.URL
	valid := false
	switch provider {
	case "codex":
		valid = u.Host == "chatgpt.com" && u.Path == "/backend-api/codex/responses" && u.RawQuery == ""
	case "claude":
		valid = u.Host == "api.anthropic.com" && u.Path == "/v1/messages" && (u.RawQuery == "" || u.RawQuery == "beta=true")
	case "gemini":
		valid = u.Host == "generativelanguage.googleapis.com" && strings.HasPrefix(u.Path, "/v1beta/models/") && (strings.HasSuffix(u.Path, ":generateContent") || strings.HasSuffix(u.Path, ":streamGenerateContent"))
		for key := range u.Query() {
			if key != "alt" && key != "$alt" {
				valid = false
			}
		}
	case "deepseek":
		valid = u.Host == "api.deepseek.com" && (u.Path == "/chat/completions" || u.Path == "/v1/chat/completions") && u.RawQuery == ""
	}
	if !valid {
		return fmt.Errorf("unsupported hosted file inference destination")
	}
	return nil
}

// OfficialFileBase rejects configured alternate origins before any screenshot upload.
func OfficialFileBase(base, origin, path string) bool {
	u, err := url.Parse(base)
	return err == nil && u.Scheme == "https" && u.Host == origin && u.User == nil && u.RawQuery == "" && u.Fragment == "" && strings.TrimRight(u.Path, "/") == path
}

// ValidateGeminiFilePlacement accepts fileData only in native multimodal part arrays.
func ValidateGeminiFilePlacement(body []byte) error {
	var root any
	if json.Unmarshal(body, &root) != nil {
		return fmt.Errorf("invalid Gemini file payload")
	}
	var walk func(any, string) error
	walk = func(value any, path string) error {
		switch v := value.(type) {
		case map[string]any:
			for key, child := range v {
				next := path + "." + key
				if key == "fileData" {
					if path != ".contents[].parts[]" && path != ".contents[].parts[].functionResponse.parts[]" {
						return fmt.Errorf("Gemini file data outside multimodal parts")
					}
					continue
				}
				if err := walk(child, next); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range v {
				if err := walk(child, path+"[]"); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(root, "")
}
