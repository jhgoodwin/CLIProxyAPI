package registry

import (
	"reflect"
	"testing"
)

func findCodexFastTestModel(models []*ModelInfo, id string) *ModelInfo {
	for _, model := range models {
		if model != nil && model.ID == id {
			return model
		}
	}
	return nil
}

func TestCodexSolFastPlans(t *testing.T) {
	for _, plan := range []struct {
		name   string
		models func() []*ModelInfo
		want   bool
	}{
		{name: "free", models: GetCodexFreeModels},
		{name: "team", models: GetCodexTeamModels, want: true},
		{name: "plus", models: GetCodexPlusModels, want: true},
		{name: "pro", models: GetCodexProModels, want: true},
	} {
		t.Run(plan.name, func(t *testing.T) {
			models := plan.models()
			base := findCodexFastTestModel(models, "gpt-6.1-sol")
			fast := findCodexFastTestModel(models, "gpt-6.1-sol-fast")
			if (base != nil) != plan.want || (fast != nil) != plan.want {
				t.Fatalf("base/fast present = %v/%v, want %v", base != nil, fast != nil, plan.want)
			}
			if !plan.want {
				return
			}
			want := cloneModelInfo(base)
			want.ID = "gpt-6.1-sol-fast"
			want.MetadataModelID = base.ID
			want.DisplayName += " Fast"
			if !reflect.DeepEqual(fast, want) {
				t.Fatalf("fast metadata = %#v, want cloned base %#v", fast, want)
			}
			count := 0
			for _, model := range WithCodexBuiltins(models) {
				if model != nil && model.ID == fast.ID {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("repeated builtins produced %d fast entries, want 1", count)
			}
		})
	}
}

func TestWithCodexBuiltinsSolFastRequiresBase(t *testing.T) {
	for _, models := range [][]*ModelInfo{nil, {nil, {ID: "gpt-6-sol"}}} {
		if fast := findCodexFastTestModel(WithCodexBuiltins(models), "gpt-6.1-sol-fast"); fast != nil {
			t.Fatalf("derived fast model without base: %#v", fast)
		}
	}
}

func TestWithCodexBuiltinsSolFastMetadataIndependent(t *testing.T) {
	webSearch := true
	base := &ModelInfo{
		ID: "gpt-6.1-sol", DisplayName: "GPT 6.1 Sol", ContextLength: 272000,
		SupportConfigurationUpdate: true,
		SupportedGenerationMethods: []string{"generateContent"},
		SupportedParameters:        []string{"tools"},
		SupportedInputModalities:   []string{"text", "image"},
		SupportedOutputModalities:  []string{"text"},
		Thinking:                   &ThinkingSupport{Levels: []string{"low", "high"}},
		Config:                     &ModelConfig{OverrideHeader: map[string]string{"user-agent": "codex-test"}},
		NativeCapabilities:         &NativeCapabilities{WebSearch: &webSearch},
	}
	original := cloneModelInfo(base)
	models := WithCodexBuiltins([]*ModelInfo{base, {ID: "gpt-6.1-sol-fast", ContextLength: 1}})
	fast := findCodexFastTestModel(models, "gpt-6.1-sol-fast")
	want := cloneModelInfo(base)
	want.ID = "gpt-6.1-sol-fast"
	want.MetadataModelID = base.ID
	want.DisplayName += " Fast"
	if !reflect.DeepEqual(fast, want) {
		t.Fatalf("fast metadata = %#v, want cloned current base %#v", fast, want)
	}
	fast.SupportedGenerationMethods[0] = "changed"
	fast.SupportedParameters[0] = "changed"
	fast.SupportedInputModalities[0] = "changed"
	fast.SupportedOutputModalities[0] = "changed"
	fast.Thinking.Levels[0] = "changed"
	fast.Config.OverrideHeader["user-agent"] = "changed"
	*fast.NativeCapabilities.WebSearch = false
	if !reflect.DeepEqual(base, original) {
		t.Fatalf("mutating fast metadata changed base: %#v", base)
	}
	// A refreshed catalog supplies only the base; derive the variant from its new metadata.
	refreshed := cloneModelInfo(base)
	refreshed.ContextLength = 1000000
	refreshed.Config.OverrideHeader["user-agent"] = "refreshed"
	refreshedFast := findCodexFastTestModel(WithCodexBuiltins([]*ModelInfo{refreshed}), "gpt-6.1-sol-fast")
	if refreshedFast == nil || refreshedFast.ContextLength != 1000000 || refreshedFast.Config.OverrideHeader["user-agent"] != "refreshed" {
		t.Fatalf("fast did not follow refreshed base metadata: %#v", refreshedFast)
	}
}

func TestCodexSolFastStaticLookup(t *testing.T) {
	for name, lookup := range map[string]func(string) *ModelInfo{
		"global": LookupStaticModelInfo,
		"codex":  func(id string) *ModelInfo { return LookupStaticModelInfoByChannel(id, "codex") },
	} {
		t.Run(name, func(t *testing.T) {
			base := lookup("gpt-6.1-sol")
			fast := lookup("gpt-6.1-sol-fast")
			if base == nil || fast == nil {
				t.Fatal("static lookup must retain both base and fast")
			}
			if fast.DisplayName != "GPT 6.1 Sol Fast" {
				t.Fatalf("display name = %q, want GPT 6.1 Sol Fast", fast.DisplayName)
			}
			fast.Thinking.Levels[0] = "changed"
			if again := lookup(fast.ID); again.Thinking.Levels[0] == "changed" {
				t.Fatal("static lookup metadata aliases a previous result")
			}
		})
	}
}
