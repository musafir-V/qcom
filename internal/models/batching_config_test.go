package models

import "testing"

func TestBatchingConfig_AppVersionOK(t *testing.T) {
	cfg := &BatchingConfig{MinDriverAppVersion: "1.4.0"}
	if cfg.AppVersionOK("") {
		t.Fatal("empty last_app_version must be old")
	}
	if cfg.AppVersionOK("1.3.9") {
		t.Fatal("1.3.9 must be below 1.4.0")
	}
	if !cfg.AppVersionOK("1.4.0") {
		t.Fatal("1.4.0 must meet min")
	}
	if !cfg.AppVersionOK("1.4.1") {
		t.Fatal("1.4.1 must meet min")
	}
	if !cfg.AppVersionOK("2.0.0") {
		t.Fatal("2.0.0 must meet min")
	}
}

func TestBatchingConfig_EmptyMinAllowsAnyNonEmpty(t *testing.T) {
	cfg := &BatchingConfig{}
	if cfg.AppVersionOK("") {
		t.Fatal("empty last is still old when min is unset")
	}
	if !cfg.AppVersionOK("0.0.1") {
		t.Fatal("any non-empty version is OK when min is unset")
	}
}
