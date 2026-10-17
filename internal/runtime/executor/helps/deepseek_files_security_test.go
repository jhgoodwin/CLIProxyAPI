package helps

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
)

type deepSeekRedirectTransport func(*http.Request) (*http.Response, error)

func (f deepSeekRedirectTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestFileUploadHTTPClientRejectsRedirects(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		for _, target := range []string{"https://api.deepseek.com/redirected", "https://api.deepseek.com.evil.test/files"} {
			called := 0
			source := &http.Client{Transport: deepSeekRedirectTransport(func(r *http.Request) (*http.Response, error) {
				called++
				if called != 1 || r.URL.String() != "https://api.deepseek.com/files" {
					t.Fatal("redirect forwarded credential or file")
				}
				if r.Header.Get("Authorization") != "Bearer selected" {
					t.Fatal("selected credential lost")
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Location": []string{target}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
			})}
			client := FileUploadHTTPClient(source)
			req, err := http.NewRequest(http.MethodPost, "https://api.deepseek.com/files", bytes.NewBufferString("private image"))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer selected")
			response, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			if errClose := response.Body.Close(); errClose != nil {
				t.Fatal(errClose)
			}
			if called != 1 || response.StatusCode != status || source.CheckRedirect != nil {
				t.Fatal("redirect policy altered source client or followed redirect")
			}
		}
	}
}

func TestProtectFileRequestHeaders(t *testing.T) {
	for _, key := range []string{"selected", ""} {
		req, err := http.NewRequest(http.MethodPost, "https://api.deepseek.com/files", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer wrong")
		req.Header.Set("Content-Type", "text/plain")
		req.Header.Set("X-Custom", "retained")
		ProtectFileRequestHeaders(req, key, "multipart/form-data; boundary=selected")
		expected := ""
		if key != "" {
			expected = "Bearer " + key
		}
		if req.Header.Get("Authorization") != expected || req.Header.Get("Content-Type") != "multipart/form-data; boundary=selected" || req.Header.Get("X-Custom") != "retained" {
			t.Fatal("protected headers not restored")
		}
	}
}
