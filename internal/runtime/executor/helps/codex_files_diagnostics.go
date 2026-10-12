package helps

import (
	"context"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// RecordFileSendDiagnostics exposes counts only, immediately before HTTP transport.
func RecordFileSendDiagnostics(ctx context.Context, body []byte, owner string) {
	c := ginContextFrom(ctx)
	if c == nil || c.Request.Header.Get(FileProbePrivacyHeader) != "1" {
		return
	}
	input := gjson.GetBytes(body, "input").Array()
	types := map[string]int{}
	for _, item := range input {
		for _, field := range []string{"content", "output"} {
			for _, part := range item.Get(field).Array() {
				switch part.Get("type").String() {
				case "input_image":
					types["input_image"]++
				case "input_text":
					types["input_text"]++
				default:
					types["other"]++
				}
			}
		}
	}
	counts := []string{}
	for _, kind := range []string{"input_image", "input_text", "other"} {
		if types[kind] > 0 {
			counts = append(counts, kind+":"+strconv.Itoa(types[kind]))
		}
	}
	same := "unknown"
	if f := FileRequestFromContext(ctx); f != nil && f.Owner != "" {
		same = "no"
		if owner == f.Owner {
			same = "yes"
		}
	}
	c.Header("X-CPA-Final-JSON-Bytes", strconv.Itoa(len(body)))
	c.Header("X-CPA-Final-Input-Count", strconv.Itoa(len(input)))
	c.Header("X-CPA-Final-Content-Types", strings.Join(counts, ","))
	c.Header("X-CPA-Same-Owner", same)
}
