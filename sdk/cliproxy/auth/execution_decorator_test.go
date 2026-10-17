package auth

import "testing"

// Embedded methods are never called: these tests exercise executor selection only.
type decorationTestExecutor struct {
	ProviderExecutor
}

type decorationTestWrapper struct {
	ProviderExecutor
	auth *Auth
}

type decorationTestFactory struct {
	ProviderExecutor
	calls int
	last  *decorationTestWrapper
}

func (e *decorationTestFactory) DecorateForAuth(auth *Auth) ProviderExecutor {
	e.calls++
	e.last = &decorationTestWrapper{ProviderExecutor: e.ProviderExecutor, auth: auth}
	return e.last
}

type decorationTestAPIKeyExecutor struct {
	ProviderExecutor
	scoped ProviderExecutor
	calls  int
}

func (e *decorationTestAPIKeyExecutor) ForAPIKey() ProviderExecutor {
	e.calls++
	return e.scoped
}

type decorationTestScopedFactory struct {
	*decorationTestFactory
	scoped ProviderExecutor
	calls  int
}

func (e *decorationTestScopedFactory) ForAPIKey() ProviderExecutor {
	e.calls++
	return e.scoped
}

func TestExecutorForAuthPreservesOrdinaryExecutor(t *testing.T) {
	for _, kind := range []string{AuthKindOAuth, AuthKindAPIKey} {
		t.Run(kind, func(t *testing.T) {
			executor := &decorationTestExecutor{}
			selected := &Auth{Attributes: map[string]string{AttributeAuthKind: kind}}
			if got := executorForAuth(executor, selected); got != executor {
				t.Fatal("ordinary executor identity changed")
			}
		})
	}
}

func TestExecutorForAuthPreservesAPIKeyScoping(t *testing.T) {
	scoped := &decorationTestExecutor{}
	executor := &decorationTestAPIKeyExecutor{scoped: scoped}
	selected := &Auth{Attributes: map[string]string{AttributeAuthKind: AuthKindAPIKey}}
	if got := executorForAuth(executor, selected); got != scoped {
		t.Fatal("API-key executor identity changed")
	}
	if executor.calls != 1 {
		t.Fatalf("ForAPIKey calls = %d, want 1", executor.calls)
	}
	selected.Attributes[AttributeAuthKind] = AuthKindOAuth
	if got := executorForAuth(executor, selected); got != executor {
		t.Fatal("OAuth executor was scoped for an API key")
	}
	if executor.calls != 1 {
		t.Fatal("ForAPIKey was called for OAuth")
	}
}

func TestExecutorForAuthBindsWrapperToSelectedAuth(t *testing.T) {
	for _, kind := range []string{AuthKindOAuth, AuthKindAPIKey} {
		t.Run(kind, func(t *testing.T) {
			base := &decorationTestExecutor{}
			executor := &decorationTestFactory{ProviderExecutor: base}
			first := &Auth{ID: "first", Attributes: map[string]string{AttributeAuthKind: kind}}
			second := &Auth{ID: "second", Attributes: map[string]string{AttributeAuthKind: kind}}
			firstWrapper := executorForAuth(executor, first).(*decorationTestWrapper)
			secondWrapper := executorForAuth(executor, second).(*decorationTestWrapper)
			if firstWrapper.auth != first || secondWrapper.auth != second {
				t.Fatal("wrapper did not retain its selected auth")
			}
			if firstWrapper == secondWrapper || executor.calls != 2 {
				t.Fatal("execution attempts did not receive separate wrappers")
			}
			if firstWrapper.ProviderExecutor != base || secondWrapper.ProviderExecutor != base {
				t.Fatal("wrapper did not retain its underlying executor")
			}
		})
	}
}

func TestExecutorForAuthDecoratesAfterAPIKeyScoping(t *testing.T) {
	base := &decorationTestExecutor{}
	scoped := &decorationTestFactory{ProviderExecutor: base}
	registeredFactory := &decorationTestFactory{}
	executor := &decorationTestScopedFactory{decorationTestFactory: registeredFactory, scoped: scoped}
	selected := &Auth{Attributes: map[string]string{AttributeAuthKind: AuthKindAPIKey}}
	got := executorForAuth(executor, selected)
	if got != scoped.last || scoped.last == nil || scoped.last.auth != selected {
		t.Fatal("API-key-scoped factory did not bind the selected auth")
	}
	if executor.calls != 1 || scoped.calls != 1 || registeredFactory.calls != 0 {
		t.Fatalf("calls: scope = %d, scoped decorator = %d, registered decorator = %d", executor.calls, scoped.calls, registeredFactory.calls)
	}
}

func TestExecutorForAuthUsesScopedExecutorWithoutDecorator(t *testing.T) {
	scoped := &decorationTestExecutor{}
	registeredFactory := &decorationTestFactory{}
	executor := &decorationTestScopedFactory{decorationTestFactory: registeredFactory, scoped: scoped}
	selected := &Auth{Attributes: map[string]string{AttributeAuthKind: AuthKindAPIKey}}
	if got := executorForAuth(executor, selected); got != scoped {
		t.Fatal("undecorated scoped executor identity changed")
	}
	if executor.calls != 1 || registeredFactory.calls != 0 {
		t.Fatal("registered decorator ran instead of using the scoped executor")
	}
}
