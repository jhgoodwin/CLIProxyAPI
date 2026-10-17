package helps

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestUploadClaudeFileMultipart(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/files" || r.Header.Get("Authorization") != "Bearer selected" {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		if err := r.ParseMultipartForm(1024); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		f, h, err := r.FormFile("file")
		if err != nil {
			t.Error(err)
			return
		}
		defer f.Close()
		data, _ := io.ReadAll(f)
		if h.Filename != "pixel.png" || string(data) != "pixel" {
			t.Errorf("unexpected upload %q %q", h.Filename, data)
		}
		fmt.Fprint(w, `{"id":"file_remote"}`)
	}))
	defer server.Close()
	id, err := UploadClaudeFile(context.Background(), server.Client(), server.URL+"/v1/files", "pixel.png", []byte("pixel"), func(r *http.Request) { r.Header.Set("Authorization", "Bearer selected") })
	if err != nil || id != "file_remote" {
		t.Fatalf("id=%q err=%v", id, err)
	}
}

func TestUploadClaudeFileRejectsResponses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{{"denied", 403, `secret upstream diagnostic`}, {"empty", 200, `{}`}, {"malformed", 200, `no json`}, {"oversized", 200, strings.Repeat("x", 65537)}, {"local", 200, `{"id":"file-cpa-bad"}`}} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) }))
			defer s.Close()
			_, err := UploadClaudeFile(context.Background(), s.Client(), s.URL, "x", []byte("x"), func(*http.Request) {})
			if err == nil {
				t.Fatal("expected rejection")
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("upstream error leaked")
			}
		})
	}
}

func TestUploadClaudeFileDoesNotFollowRedirects(t *testing.T) {
	var calls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer origin.Close()
	_, err := UploadClaudeFile(context.Background(), origin.Client(), origin.URL, "x", []byte("x"), func(r *http.Request) { r.Header.Set("Authorization", "Bearer secret") })
	if err == nil || calls.Load() != 0 {
		t.Fatalf("err=%v target calls=%d", err, calls.Load())
	}
}

func TestUploadClaudeFileCancellation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-release }))
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := UploadClaudeFile(ctx, s.Client(), s.URL, "x", []byte("x"), func(*http.Request) {})
		result <- err
	}()
	<-started
	cancel()
	err := <-result
	close(release)
	if err == nil {
		t.Fatal("expected cancellation")
	}
}
