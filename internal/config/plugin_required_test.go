package config

import (
	"gopkg.in/yaml.v3"
	"testing"
)

func TestPluginRequiredConfigSurvivesYAMLRoundTrip(t *testing.T) {
	var item PluginInstanceConfig
	if err := yaml.Unmarshal([]byte("enabled: true\nrequired: true\nstore:\n  version: 1.0.0\n"), &item); err != nil {
		t.Fatal(err)
	}
	if !item.Required {
		t.Fatal("required config not parsed")
	}
	encoded, err := yaml.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	var got PluginInstanceConfig
	if err = yaml.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Required {
		t.Fatalf("required config lost: %s", encoded)
	}
	item.Required = false
	encoded, err = yaml.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	if err = yaml.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if got.Required {
		t.Fatalf("explicitly disabled required setting retained: %s", encoded)
	}
}

func TestProgrammaticRequiredPluginConfigPersists(t *testing.T) {
	item := PluginInstanceConfig{Required: true}
	encoded, err := yaml.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	var got PluginInstanceConfig
	if err = yaml.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Required {
		t.Fatalf("required config lost: %s", encoded)
	}
	if err = yaml.Unmarshal([]byte("required: invalid"), &got); err == nil {
		t.Fatal("invalid required setting accepted")
	}
}
