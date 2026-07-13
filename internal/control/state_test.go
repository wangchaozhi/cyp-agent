package control

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/wangchaozhi/cyp-agent/internal/config"
	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

func TestUpdateSettingsIsRedactedAndEmptyKeyDoesNotClear(t *testing.T) {
	settings := config.DefaultSettings()
	settings.DeepSeekAPIKey = config.Secret("old-secret")
	settings.LLMBaseURL = "https://api.deepseek.com"
	state := New(settings)
	provider := "deepseek"
	empty := "  "
	baseURL := " https://api.deepseek.com "
	if err := state.UpdateSettings(contracts.SettingsUpdateRequest{
		LLMProvider: &provider, LLMBaseURL: &baseURL, DeepSeekAPIKey: &empty,
	}); err != nil {
		t.Fatalf("UpdateSettings() error = %v", err)
	}
	if !state.Settings().DeepSeekAPIKey.Configured() || state.Settings().LLMBaseURL != "https://api.deepseek.com" {
		t.Fatalf("unexpected settings: %s", state.Settings())
	}
	encoded, err := json.Marshal(state.Snapshot())
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if strings.Contains(string(encoded), "old-secret") {
		t.Fatalf("secret leaked in snapshot: %s", encoded)
	}
}

func TestRuntimeBaseURLCannotRedirectExistingSecret(t *testing.T) {
	settings := config.DefaultSettings()
	settings.DeepSeekAPIKey = config.Secret("old-secret")
	state := New(settings)
	baseURL := "https://attacker.invalid"
	model := "must-not-commit"
	if err := state.UpdateSettings(contracts.SettingsUpdateRequest{
		LLMBaseURL: &baseURL, LLMModel: &model,
	}); !errors.Is(err, ErrRuntimeLLMBaseURL) {
		t.Fatalf("UpdateSettings() error = %v", err)
	}
	if state.Settings().LLMModel == model || state.Settings().DeepSeekAPIKey.Reveal() != "old-secret" {
		t.Fatal("rejected base URL update partially mutated settings")
	}
}

func TestInvalidProviderDoesNotPartiallyMutate(t *testing.T) {
	state := New(config.DefaultSettings())
	provider := "other"
	model := "changed"
	if err := state.UpdateSettings(contracts.SettingsUpdateRequest{LLMProvider: &provider, LLMModel: &model}); err == nil {
		t.Fatal("UpdateSettings() unexpectedly succeeded")
	}
	if state.Settings().LLMModel == "changed" {
		t.Fatal("invalid request partially mutated state")
	}
}

func TestRuntimeModeUpdateKeepsLiveExecutionReadOnly(t *testing.T) {
	state := New(config.DefaultSettings())
	live := " LIVE "
	if err := state.UpdateSettings(contracts.SettingsUpdateRequest{Mode: &live}); err != nil {
		t.Fatalf("UpdateSettings() error = %v", err)
	}
	settings := state.Settings()
	if settings.Mode != "live" {
		t.Fatalf("mode = %q, want live", settings.Mode)
	}
	if settings.LiveGuard().OK || settings.LiveExecutionAllowed() {
		t.Fatal("runtime mode update bypassed the live execution safety rail")
	}

	invalid := "production"
	if err := state.UpdateSettings(contracts.SettingsUpdateRequest{Mode: &invalid}); !errors.Is(err, ErrInvalidRuntimeMode) {
		t.Fatalf("invalid mode error = %v", err)
	}
	if state.Settings().Mode != "live" {
		t.Fatal("invalid mode partially mutated settings")
	}
}
