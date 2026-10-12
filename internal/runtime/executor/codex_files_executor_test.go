package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestCodexFilesUploadProtocol(t *testing.T) {
	for _, final := range []string{`{"status":"success","download_url":"https://example.invalid/file"}`, `{"status":"success","download_url":"https://example.invalid/reservation"}`, `{}`, `{"status":"retry","success":true}`, `{"status":"success"}`, `{"success":false}`, `{"reject":"create"}`, `{"reject":"blob"}`, `{"reject":"finalize"}`} {
		t.Run(final, func(t *testing.T) {
			data := []byte("unchanged PNG bytes")
			stages := 0
			blob := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "PUT" || r.Header.Get("Authorization") != "" || r.Header.Get("ChatGPT-Account-ID") != "" || r.Header.Get("X-Secret") != "" {
					t.Error("blob received credentials/wrong method")
				}
				if r.Header.Get("x-ms-blob-type") != "BlockBlob" {
					t.Error("missing blob header")
				}
				if _, err := uuid.Parse(r.Header.Get("x-ms-client-request-id")); err != nil {
					t.Error("invalid uuid")
				}
				body, _ := io.ReadAll(r.Body)
				if !bytes.Equal(body, data) {
					t.Error("modified image")
				}
				stages++
				if final == `{"reject":"blob"}` {
					w.WriteHeader(403)
					return
				}
				w.WriteHeader(201)
			}))
			defer blob.Close()
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer fake" || r.Header.Get("ChatGPT-Account-ID") != "synthetic" {
					t.Error("missing backend credentials")
				}
				stages++
				if r.URL.Path == "/files" {
					if final == `{"reject":"create"}` {
						w.WriteHeader(400)
						return
					}
					var req map[string]any
					_ = json.NewDecoder(r.Body).Decode(&req)
					if req["use_case"] != "codex" || req["file_name"] != "sample.png" || req["file_size"] != float64(len(data)) {
						t.Error("invalid reservation")
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"file_id": "upstream-id", "upload_url": blob.URL + "/blob?sig=never-log", "pdf_c2pa_reservation": final == `{"status":"success","download_url":"https://example.invalid/reservation"}`})
					return
				}
				if r.URL.Path != "/files/upstream-id/uploaded" {
					t.Error("wrong finalize")
				}
				body, _ := io.ReadAll(r.Body)
				if final == `{"status":"success","download_url":"https://example.invalid/reservation"}` {
					var got struct {
						Create map[string]any `json:"pdf_c2pa_create_request"`
					}
					if json.Unmarshal(body, &got) != nil || got.Create["file_name"] != "sample.png" || got.Create["file_size"] != float64(len(data)) || got.Create["use_case"] != "codex" {
						t.Error("reservation finalization missing original create")
					}
				} else if string(body) != "{}" {
					t.Error("PNG finalization body")
				}
				if final == `{"reject":"finalize"}` {
					w.WriteHeader(400)
					return
				}
				_, _ = w.Write([]byte(final))
			}))
			defer backend.Close()
			a := &auth.Auth{ID: "owner", Provider: "codex", Attributes: map[string]string{"base_url": backend.URL + "/codex", "header:X-Secret": "never"}, Metadata: map[string]any{"access_token": "fake", "account_id": "synthetic"}}
			id, err := NewCodexExecutor(&config.Config{}).UploadVisionPNG(context.Background(), a, "sample.png", data)
			if final == `{"status":"success","download_url":"https://example.invalid/file"}` || final == `{"status":"success","download_url":"https://example.invalid/reservation"}` {
				if err != nil || id != "upstream-id" {
					t.Fatalf("id=%q err=%v", id, err)
				}
			} else if err == nil || id != "" {
				t.Fatal("success before ready")
			}
			wantStages := 3
			if final == `{"reject":"create"}` {
				wantStages = 1
			}
			if final == `{"reject":"blob"}` {
				wantStages = 2
			}
			if stages != wantStages {
				t.Fatalf("stages=%d want=%d", stages, wantStages)
			}
		})
	}
}
