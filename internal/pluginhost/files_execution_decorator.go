package pluginhost

import coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"

// DecorateForAuth preserves plugin refresh ownership while adapting native inference.
func (e pluginRefreshCompatExecutor) DecorateForAuth(auth *coreauth.Auth) coreauth.ProviderExecutor {
	if decorator, ok := e.inner.(coreauth.AuthExecutionDecorator); ok {
		e.inner = decorator.DecorateForAuth(auth)
		return &e
	}
	return &e
}
