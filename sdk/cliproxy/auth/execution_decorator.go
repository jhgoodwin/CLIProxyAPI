package auth

// AuthExecutionDecorator optionally provides an execution-local wrapper bound to
// the selected auth. Manager invokes it after API-key configuration scoping.
// Implementations must leave the registered executor and shared state intact.
type AuthExecutionDecorator interface {
	// DecorateForAuth returns a non-nil executor for the selected auth. The wrapper
	// is local to this execution attempt and must not select another credential.
	DecorateForAuth(auth *Auth) ProviderExecutor
}
