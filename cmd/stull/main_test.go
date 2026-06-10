package main

import "testing"

// The git tag is the single source of truth: an ldflags-baked version must win
// over every build-metadata fallback. (The fallback chain itself is
// environment-dependent — verified by building in the three real modes — so
// this locks the one branch that must be deterministic.)
func TestBuildVersionPrefersLdflags(t *testing.T) {
	old := version
	defer func() { version = old }()

	version = "v1.2.3"
	if got := buildVersion(); got != "v1.2.3" {
		t.Fatalf("ldflags-baked version must win, got %q", got)
	}
}
