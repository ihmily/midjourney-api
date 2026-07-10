package main

import "testing"

func TestDefaultConfigPathUsesEnvironmentOverride(t *testing.T) {
	t.Setenv("MJ_CONFIG", "  /tmp/mj/config.yaml  ")

	if got := defaultConfigPath(); got != "/tmp/mj/config.yaml" {
		t.Fatalf("defaultConfigPath = %q, want env override", got)
	}
}

func TestDefaultConfigPathFallsBackToConfigYAML(t *testing.T) {
	t.Setenv("MJ_CONFIG", "   ")

	if got := defaultConfigPath(); got != "config/config.yaml" {
		t.Fatalf("defaultConfigPath = %q, want default config path", got)
	}
}
