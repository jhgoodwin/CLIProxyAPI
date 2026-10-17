package helps

import (
	"encoding/json"
	"fmt"
)

// ValidateOpenAIFilePlacement accepts file IDs only in Responses message image content.
// Payload rules may remove images, but cannot move owned IDs into tools or arbitrary fields.
func ValidateOpenAIFilePlacement(body []byte) error {
	var root map[string]any
	if json.Unmarshal(body, &root) != nil {
		return fmt.Errorf("invalid official OpenAI file payload")
	}
	permitted := map[string]int{}
	input, _ := root["input"].([]any)
	for _, value := range input {
		item, _ := value.(map[string]any)
		typ, _ := item["type"].(string)
		if typ != "" && typ != "message" {
			continue
		}
		role, _ := item["role"].(string)
		if role != "user" && role != "assistant" && role != "system" && role != "developer" {
			continue
		}
		parts, _ := item["content"].([]any)
		for _, value := range parts {
			part, _ := value.(map[string]any)
			if part["type"] != "input_image" {
				continue
			}
			id, ok := part["file_id"].(string)
			if !ok || id == "" {
				return fmt.Errorf("invalid official OpenAI image file")
			}
			for key := range part {
				if key != "type" && key != "file_id" && key != "detail" {
					return fmt.Errorf("invalid official OpenAI image fields")
				}
			}
			permitted[id]++
		}
	}
	var walk func(any) error
	walk = func(value any) error {
		switch v := value.(type) {
		case map[string]any:
			for key, child := range v {
				if key == "file_id" {
					id, ok := child.(string)
					if !ok || permitted[id] == 0 {
						return fmt.Errorf("official OpenAI file reference outside image content")
					}
					permitted[id]--
				}
				if err := walk(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range v {
				if err := walk(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(root)
}
