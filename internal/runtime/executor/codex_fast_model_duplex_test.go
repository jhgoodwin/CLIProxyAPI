package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	core "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexSolFastDuplexSteeringPayloadRule(t *testing.T) {
	frames := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.Close() }()
		// This is a test failure bound, not a production network deadline.
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		if _, _, err := conn.ReadMessage(); err != nil {
			t.Error(err)
			return
		}
		if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"r1","output":[]}}`)); err != nil {
			t.Error(err)
			return
		}
		_, steer, err := conn.ReadMessage()
		if err != nil {
			t.Error(err)
			return
		}
		frames <- steer
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	input := make(chan core.WebsocketInput, 1)
	ctx = core.WithWebsocketInput(core.WithDownstreamWebsocket(ctx), input)
	exec := NewCodexWebsocketsExecutor(&config.Config{
		SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll},
		Codex:     config.CodexConfig{ResponseSteering: true},
		Payload: config.PayloadConfig{Filter: []config.PayloadFilterRule{{
			Models: []config.PayloadModelRule{{Name: "gpt-6.1-sol", Protocol: "codex"}},
			Params: []string{"private_marker"},
		}}},
	})
	exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
	auth := codexOAuthTestAuth(server.URL)
	auth.ID = "fast-duplex-test"
	opts := core.Options{SourceFormat: sdktranslator.FromString("codex"), Metadata: map[string]any{core.ExecutionSessionMetadataKey: "fast-duplex-test"}}
	result, err := exec.ExecuteStream(ctx, auth, core.Request{Model: "gpt-6.1-sol-fast", Payload: []byte(`{"model":"gpt-6.1-sol-fast","input":[]}`)}, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		for range result.Chunks {
		}
	}()
	for {
		select {
		case chunk, ok := <-result.Chunks:
			if !ok {
				t.Fatal("stream closed before steering")
			}
			if chunk.Err != nil {
				t.Fatal(chunk.Err)
			}
			if gjson.GetBytes(chunk.Payload, "type").String() == "response.created" {
				input <- core.WebsocketInput{Payload: []byte(`{"type":"response.steer","previous_response_id":"r1","input":"adjust","private_marker":"secret"}`)}
			}
		case frame := <-frames:
			if gjson.GetBytes(frame, "private_marker").Exists() {
				t.Fatalf("upstream-name filter missed fast-model steer: %s", frame)
			}
			if gjson.GetBytes(frame, "service_tier").Exists() || gjson.GetBytes(frame, "model").Exists() {
				t.Fatalf("create defaults leaked into steer: %s", frame)
			}
			if gjson.GetBytes(frame, "type").String() != "response.steer" {
				t.Fatalf("wrong frame: %s", frame)
			}
			return
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}
