package helps

import (
	"encoding/json"
	"strings"
)

// SafeFileRejection retains the diagnostic namespace message, not arbitrary server data.
func SafeFileRejection(body []byte) string {
	var v struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
		Detail  string `json:"detail"`
	}
	if json.Unmarshal(body, &v) != nil {
		return ""
	}
	message := v.Error.Message
	if message == "" {
		message = v.Message
	}
	if message == "" {
		message = v.Detail
	}
	switch strings.ToLower(message) {
	case "file not found", "file does not exist", "file does not exist in namespace", "file not found in namespace", "file not found in the codex namespace", "file does not exist in the codex namespace":
		return message
	default:
		return ""
	}
}
