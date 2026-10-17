package executor

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	execution "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	translator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestFilesDecorationPreservesKimiRouting(t *testing.T) {
	original := NewKimiExecutor(&config.Config{})
	selected := &auth.Auth{Provider: "kimi", Attributes: map[string]string{"api_key": "test"}}
	for _, scoped := range []bool{false, true} {
		var executor auth.ProviderExecutor = original
		if scoped {
			executor = executor.(auth.APIKeyConfigExecutor).ForAPIKey()
			if executor == original {
				t.Fatal("API-key scoping reused the registered executor")
			}
		}
		decorated := executor.(auth.AuthExecutionDecorator).DecorateForAuth(selected)
		kimi, ok := decorated.(*KimiExecutor)
		if !ok || decorated != executor || decorated.Identifier() != "kimi" {
			t.Fatalf("scoped=%v: decoration replaced Kimi with %T", scoped, decorated)
		}
		for _, source := range []translator.Format{translator.FormatOpenAIResponse, translator.FormatOpenAI, translator.FormatClaude} {
			if got := kimi.RequestToFormat(execution.Request{}, execution.Options{SourceFormat: source}); got != source {
				t.Fatalf("scoped=%v: source=%s routed to %s", scoped, source, got)
			}
		}
	}
}

func TestFilesDecorationPreservesCodexWebsocketTransport(t *testing.T) {
	original := NewCodexWebsocketsExecutor(&config.Config{})
	selected := &auth.Auth{Provider: "codex", Attributes: map[string]string{"api_key": "test"}}
	for _, scoped := range []bool{false, true} {
		var executor auth.ProviderExecutor = original
		if scoped {
			executor = executor.(auth.APIKeyConfigExecutor).ForAPIKey()
			if executor == original {
				t.Fatal("API-key scoping reused the registered executor")
			}
		}
		decorated := executor.(auth.AuthExecutionDecorator).DecorateForAuth(selected)
		websocket, ok := decorated.(*CodexWebsocketsExecutor)
		if !ok || decorated != executor {
			t.Fatalf("scoped=%v: decoration replaced WebSocket executor with %T", scoped, decorated)
		}
		if websocket.store != original.store {
			t.Fatal("decoration replaced the WebSocket session store")
		}
		if scoped && websocket.CodexExecutor == original.CodexExecutor {
			t.Fatal("API-key scoping reused the embedded HTTP executor")
		}
	}
}
