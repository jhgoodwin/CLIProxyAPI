package claude

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// claudeToolImageRelay keeps each image group associated with its tool result.
// Chat tool messages are text-only; correlation travels in the following user turn.
func claudeToolImageRelay(id string, images [][]byte) [][]byte {
	if len(images) == 0 {
		return nil
	}
	label := []byte(`{"type":"text","text":""}`)
	label, _ = sjson.SetBytes(label, "text", "Images from tool result "+id+":")
	return append([][]byte{label}, images...)
}

// nestedClaudeToolContent unwraps content containers, not arbitrary tool data.
// In particular, image source objects and textual JSON outputs remain opaque.
func nestedClaudeToolContent(item gjson.Result) (gjson.Result, bool) {
	if item.IsArray() {
		return item, true
	}
	if item.IsObject() && item.Get("type").String() != "image" && item.Get("type").String() != "text" {
		content := item.Get("content")
		if content.IsArray() || content.IsObject() {
			return content, true
		}
	}
	return gjson.Result{}, false
}
