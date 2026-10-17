package claude

import (
	"github.com/tidwall/gjson"
	"strings"
	"testing"
)

func TestNestedToolImagesPreserveCorrelation(t *testing.T) {
	input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"first","input":{}},{"type":"tool_use","id":"b","name":"second","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"b","content":{"content":[[{"type":"image","source":{"type":"url","url":"https://example.com/b.png"}}]]}},{"type":"tool_result","tool_use_id":"a","content":[{"content":[{"type":"text","text":"first result"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"YQ=="}}]}]}]}]}`)
	out, err := ConvertClaudeRequestToOpenAIWithCompat("compatible", input, false)
	if err != nil {
		t.Fatal(err)
	}
	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 4 {
		t.Fatalf("unexpected messages: %s", out)
	}
	for i, id := range []string{"a", "b"} {
		if messages[i+1].Get("tool_call_id").String() != id {
			t.Fatalf("tool correlation lost: %s", out)
		}
	}
	relay := messages[3].Get("content").Array()
	if len(relay) != 5 || !strings.Contains(relay[1].Get("text").String(), "a") || !strings.Contains(relay[3].Get("text").String(), "b") {
		t.Fatalf("image correlation lost: %s", out)
	}
	if relay[2].Get("image_url.url").String() != "data:image/png;base64,YQ==" || relay[4].Get("image_url.url").String() != "https://example.com/b.png" {
		t.Fatalf("nested images lost: %s", out)
	}
	if messages[1].Get("content").String() != "first result" {
		t.Fatalf("nested text lost: %s", out)
	}
}
