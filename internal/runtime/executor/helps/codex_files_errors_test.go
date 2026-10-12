package helps

import "testing"

func TestSafeFileNamespaceDiagnostic(t *testing.T) {
	body := []byte(`{"error":{"message":"File not found in namespace"}}`)
	if got := SafeFileRejection(body); got != "File not found in namespace" {
		t.Fatalf("message=%q", got)
	}
	for _, body := range []string{`{"error":{"message":"File not found in namespace https://blob?sig=secret"}}`, `{"error":{"message":"File file-secret123 not found in namespace"}}`, `{"error":{"message":"File not found in account namespace"}}`} {
		if got := SafeFileRejection([]byte(body)); got != "" {
			t.Fatalf("leaked %q", got)
		}
	}
}
