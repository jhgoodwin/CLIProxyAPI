package helps

import "context"

// SummarizeErrorBodyForRequest avoids logging provider error bodies for file-private
// requests, including every operation on the Anthropic compatibility surface.
func SummarizeErrorBodyForRequest(ctx context.Context, contentType string, body []byte) string {
	if FilePrivacyContext(ctx) {
		return "[upstream error body withheld for privacy]"
	}
	return SummarizeErrorBody(contentType, body)
}
