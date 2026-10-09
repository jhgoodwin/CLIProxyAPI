package pluginhost

import "testing"

func TestRequiredPluginConfigConversionPreservesGuard(t *testing.T) {
	converted := pluginConfigsToInternal(map[string]PluginInstanceConfig{"tenant": {Required: true}})
	if !converted["tenant"].Required {
		t.Fatal("public SDK dropped required plugin guard")
	}
}
