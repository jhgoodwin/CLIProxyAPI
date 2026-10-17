package helps

import "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"

const codexSolFastModel = "gpt-6.1-sol-fast"

// CodexBaseModel resolves the selectable fast variant to its upstream model.
func CodexBaseModel(model string) string {
	base := thinking.ParseSuffix(model).ModelName
	if base == codexSolFastModel {
		return "gpt-6.1-sol"
	}
	return base
}

// ApplyCodexFastModelTier selects priority before the final user payload rules.
func ApplyCodexFastModelTier(body []byte, model string) []byte {
	if thinking.ParseSuffix(model).ModelName == codexSolFastModel {
		return SetStringIfDifferent(body, "service_tier", "priority")
	}
	return body
}
