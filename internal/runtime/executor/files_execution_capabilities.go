package executor

import (
	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	execution "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	translator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// RequestToFormat preserves auth-selected native formats used by request interceptors.
// An empty result retains the manager's fallback for executors without a resolver.
func (e *filesExecutionDecorator) RequestToFormat(req execution.Request, opts execution.Options) translator.Format {
	resolver, ok := e.ProviderExecutor.(interface {
		RequestToFormat(execution.Request, execution.Options) translator.Format
	})
	if !ok {
		return ""
	}
	return resolver.RequestToFormat(req, opts)
}

// SupportsApplyPatch reports the original executor's capability, without enabling it implicitly.
func (e *filesExecutionDecorator) SupportsApplyPatch() bool {
	support, ok := e.ProviderExecutor.(auth.ApplyPatchSupport)
	return ok && support.SupportsApplyPatch()
}
