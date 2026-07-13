package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultSettingsArePaperAndLiveExecutionIsCompileTimeDisabled(t *testing.T) {
	t.Parallel()
	settings := DefaultSettings()
	if settings.Mode != "paper" || settings.ExecutionVenue != "paper" {
		t.Fatalf("unsafe defaults: mode=%s venue=%s", settings.Mode, settings.ExecutionVenue)
	}
	if !settings.LiveGuard().OK || !settings.NewPaperPositionAllowed() {
		t.Fatal("default paper settings should permit a new paper position")
	}
	if LiveExecutionSupported || settings.LiveExecutionAllowed() {
		t.Fatal("first Go slice must never enable live execution")
	}

	settings.Mode = "live"
	settings.LiveAck = true
	settings.BinanceAPIKey = "key"
	settings.BinanceAPISecret = "secret"
	report := settings.LiveGuard()
	if report.OK || settings.LiveExecutionAllowed() {
		t.Fatal("credentials and acknowledgement must not unlock first-release live execution")
	}
	if !strings.Contains(strings.Join(report.Reasons, " "), "首版硬禁实盘") {
		t.Fatalf("hard-disable reason missing: %v", report.Reasons)
	}
}

func TestOnlyConfiguredOKXDemoUnlocksCEXExecution(t *testing.T) {
	t.Parallel()
	settings := DefaultSettings()
	settings.ExecutionVenue = "okx"
	settings.OKXDemo = true
	settings.OKXAPIKey = "demo-key"
	settings.OKXAPISecret = "demo-secret"
	settings.OKXPassword = "demo-passphrase"
	if !settings.OKXDemoExecutionConfigured() || !settings.NewPositionAllowed() || !settings.LiveGuard().OK {
		t.Fatalf("configured Demo account should be executable: %#v", settings.LiveGuard())
	}
	settings.OKXDemo = false
	if settings.NewPositionAllowed() || settings.LiveGuard().OK {
		t.Fatal("production OKX must stay hard-disabled")
	}
	settings.OKXDemo = true
	settings.Mode = "live"
	if settings.NewPositionAllowed() || settings.LiveGuard().OK || settings.LiveExecutionAllowed() {
		t.Fatal("live mode must stay hard-disabled even with Demo credentials")
	}
}

func TestLoadDotEnvAndEnvironmentTakesPrecedence(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), ".env")
	contents := strings.Join([]string{
		"# comment",
		"export CYP_MODE=paper",
		"CYP_WATCHLIST='BTC/USDT, ETH/USDT'",
		"CYP_MAX_POSITION_PCT=0.15",
		"CYP_KILL=no",
		`CYP_LLM_MODEL="model\nname" # supported quoting`,
		"ANTHROPIC_API_KEY=dotenv-secret",
	}, "\n")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	environment := map[string]string{
		"CYP_KILL":             "1",
		"CYP_MAX_LEVERAGE":     "2.50",
		"CYP_MAX_MARGIN_PCT":   "0.08",
		"CYP_LEVERAGE_STEP":    "0.5",
		"CYP_LIQ_VOL_MULTIPLE": "4",
		"CYP_LIQ_RESERVE_PCT":  "0.03",
		"ANTHROPIC_API_KEY":    "environment-secret",
	}
	settings, err := LoadWithOptions(LoadOptions{
		EnvFile: path,
		LookupEnv: func(key string) (string, bool) {
			value, ok := environment[key]
			return value, ok
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !settings.Kill || settings.Risk.MaxPositionPct.String() != "0.15" || settings.Risk.MaxLeverage.String() != "2.50" {
		t.Fatalf("unexpected loaded settings: kill=%v position=%s leverage=%s", settings.Kill,
			settings.Risk.MaxPositionPct, settings.Risk.MaxLeverage)
	}
	if settings.Risk.MaxMarginPct.String() != "0.08" || settings.Risk.LeverageStep.String() != "0.5" ||
		settings.Risk.LiqVolMultiple.String() != "4" || settings.Risk.LiqReservePct.String() != "0.03" {
		t.Fatalf("leverage model config not loaded: %+v", settings.Risk)
	}
	if settings.AnthropicAPIKey.Reveal() != "environment-secret" {
		t.Fatal("environment did not override dotenv secret")
	}
	if got := settings.WatchlistSymbols(); len(got) != 2 || got[1] != "ETH/USDT" {
		t.Fatalf("watchlist = %v", got)
	}
	if settings.LLMModel != "model\nname" {
		t.Fatalf("quoted dotenv decoding = %q", settings.LLMModel)
	}
}

func TestSettingsSerializationIsRedacted(t *testing.T) {
	t.Parallel()
	settings := DefaultSettings()
	settings.AnthropicAPIKey = "anthropic-super-secret"
	settings.BinanceAPISecret = "binance-super-secret"
	settings.APIToken = "api-super-secret"
	settings.DBURL = "postgresql://user:database-password@localhost/db"
	encoded, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"anthropic-super-secret", "binance-super-secret", "api-super-secret", "database-password"} {
		if strings.Contains(string(encoded), secret) || strings.Contains(settings.String(), secret) {
			t.Fatalf("secret %q leaked in settings output", secret)
		}
	}
	if !settings.Snapshot().LLMEnabled {
		t.Fatal("snapshot should expose configured state without exposing value")
	}
}

func TestLoadRejectsInvalidValuesWithoutFallback(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"CYP_MODE":               "production",
		"CYP_KILL":               "sometimes",
		"CYP_MAX_RISK_PER_TRADE": "NaN",
		"CYP_MAX_CONCURRENCY":    "0",
		"CYP_MAX_MARGIN_PCT":     "1.1",
		"CYP_LEVERAGE_STEP":      "2",
	}
	for key, value := range tests {
		key, value := key, value
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			_, err := LoadWithOptions(LoadOptions{
				LookupEnv: func(candidate string) (string, bool) {
					return value, candidate == key
				},
			})
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("Load error = %v, want key %s", err, key)
			}
		})
	}
}

func TestMissingDotEnvIsAllowed(t *testing.T) {
	t.Parallel()
	settings, err := LoadWithOptions(LoadOptions{
		EnvFile:   filepath.Join(t.TempDir(), "missing.env"),
		LookupEnv: func(string) (string, bool) { return "", false },
	})
	if err != nil {
		t.Fatal(err)
	}
	if settings.Mode != "paper" {
		t.Fatalf("mode = %s", settings.Mode)
	}
}

func TestLegacyAutoApprovalMigratesButExplicitMasterWins(t *testing.T) {
	t.Parallel()
	legacy, err := LoadWithOptions(LoadOptions{LookupEnv: func(key string) (string, bool) {
		return "auto", key == "CYP_APPROVAL"
	}})
	if err != nil || !legacy.Automation.Enabled || !legacy.Automation.ApprovalEnabled {
		t.Fatalf("legacy migration automation=%#v err=%v", legacy.Automation, err)
	}
	explicit, err := LoadWithOptions(LoadOptions{LookupEnv: func(key string) (string, bool) {
		values := map[string]string{"CYP_APPROVAL": "auto", "CYP_AUTOMATION_ENABLED": "false"}
		value, ok := values[key]
		return value, ok
	}})
	if err != nil || explicit.Automation.Enabled {
		t.Fatalf("explicit master automation=%#v err=%v", explicit.Automation, err)
	}
}

func TestRepositoryDotEnvExampleLoads(t *testing.T) {
	t.Parallel()
	settings, err := LoadWithOptions(LoadOptions{
		EnvFile:   filepath.Join("..", "..", ".env.example"),
		LookupEnv: func(string) (string, bool) { return "", false },
	})
	if err != nil {
		t.Fatalf(".env.example must stay compatible with the Go loader: %v", err)
	}
	if settings.Mode != "paper" || settings.Risk.MaxRiskPerTrade.String() != "0.01" {
		t.Fatalf("unexpected example settings: mode=%s risk=%s", settings.Mode, settings.Risk.MaxRiskPerTrade)
	}
}
