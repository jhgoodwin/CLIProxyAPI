package executor

import (
	"testing"

	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	execution "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	translator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type fileCapabilityExecutor struct {
	auth.ProviderExecutor
	format    translator.Format
	patch     bool
	seenModel string
}

func (e *fileCapabilityExecutor) RequestToFormat(r execution.Request, _ execution.Options) translator.Format {
	e.seenModel = r.Model
	return e.format
}
func (e *fileCapabilityExecutor) SupportsApplyPatch() bool { return e.patch }

func TestFilesDecoratorPreservesSelectedNativeFormat(t *testing.T) {
	for _, patch := range []bool{false, true} {
		original := &fileCapabilityExecutor{format: translator.Format("gemini-interactions"), patch: patch}
		wrapper := &filesExecutionDecorator{ProviderExecutor: original}
		if got := wrapper.RequestToFormat(execution.Request{Model: "selected-model"}, execution.Options{}); got != original.format || original.seenModel != "selected-model" {
			t.Fatal("selected native format erased")
		}
		if wrapper.SupportsApplyPatch() != patch {
			t.Fatal("apply patch capability changed")
		}
	}
}
func TestFilesDecoratorAbsentCapabilitiesRetainFallback(t *testing.T) {
	wrapper := &filesExecutionDecorator{ProviderExecutor: &filesDelegateSpy{}}
	if wrapper.RequestToFormat(execution.Request{}, execution.Options{}) != "" || wrapper.SupportsApplyPatch() {
		t.Fatal("absent capability enabled")
	}
}
