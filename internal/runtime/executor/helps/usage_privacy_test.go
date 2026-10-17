package helps

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

type privateUsageDiagnostic struct {
	usageResponseBodyError
}

func (privateUsageDiagnostic) LogDiagnostic() string {
	return "upstream request failed (HTTP 429)"
}

func TestFilePrivacyUsageFailureKeepsStatusAndSuppressesBody(t *testing.T) {
	body := []byte(`{"error":{"code":"synthetic_limit","type":"rate_limit_error","message":"PRIVATE_SYNTHETIC https://example.invalid/blob?sig=POISON"}}`)
	original := privateUsageDiagnostic{usageResponseBodyError{status: http.StatusTooManyRequests, message: string(body), body: body}}
	for _, err := range []error{original, fmt.Errorf("execute failed: %w", original)} {
		failure := failFromErrors(err)
		if failure.StatusCode != http.StatusTooManyRequests || failure.Body != original.LogDiagnostic() {
			t.Fatal("private usage failure lost status or persisted original body")
		}
		if !strings.Contains(err.Error(), "PRIVATE_SYNTHETIC") || string(original.ResponseBody()) != string(body) {
			t.Fatal("usage logging changed the original client error")
		}
	}
	ordinary := original.usageResponseBodyError
	failure := failFromErrors(ordinary)
	if failure.StatusCode != http.StatusTooManyRequests || failure.Body != string(body) {
		t.Fatal("ordinary usage failure changed")
	}
}
