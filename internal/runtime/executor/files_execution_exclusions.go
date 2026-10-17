package executor

import auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"

// DecorateForAuth preserves Kimi's protocol routing instead of decorating its
// embedded Claude executor. Kimi does not support hosted screenshot Files.
func (e *KimiExecutor) DecorateForAuth(_ *auth.Auth) auth.ProviderExecutor {
	return e
}

// DecorateForAuth preserves WebSocket execution instead of decorating its
// embedded HTTP executor. Hosted screenshot Files require HTTP generation.
func (e *CodexWebsocketsExecutor) DecorateForAuth(_ *auth.Auth) auth.ProviderExecutor {
	return e
}
