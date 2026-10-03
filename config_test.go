package gridcore

import (
	"os"
	"path/filepath"
	"testing"
)

var configEnvKeys = []string{
	"EXCHANGE_NAME", "NETWORK", "SYMBOL", "DRY_RUN", "PRE_RUN", "DRY_RUN_MARK_PRICE",
	"ORDER_SIZE", "GRID_SPACING_PERCENTAGE", "PROFIT_PERCENTAGE", "NUM_GRID_LEVELS",
	"POLL_INTERVAL_SECONDS", "MARGIN_CHECK_INTERVAL_SECONDS", "HTTP_TIMEOUT_SECONDS",
	"TELEGRAM_TOKEN", "TELEGRAM_CHAT_ID",
}

func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, k := range configEnvKeys {
		t.Setenv(k, "")
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	clearConfigEnv(t)
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig defaults: %v", err)
	}
	if cfg.ExchangeName != "Lighter" || cfg.Network != "mainnet" || cfg.Symbol != "BTC-USDT-PERP" {
		t.Fatalf("unexpected identity defaults: %+v", cfg)
	}
	if cfg.DryRun || cfg.PreRun {
		t.Fatalf("expected DryRun/PreRun false, got %+v", cfg)
	}
	if cfg.DryRunMarkPrice != 100000 || cfg.OrderSize != 0.01 || cfg.GridSpacing != 0.2 || cfg.ProfitPct != 0.2 {
		t.Fatalf("unexpected numeric defaults: %+v", cfg)
	}
	if cfg.NumLevels != 5 || cfg.PollSeconds != 3 || cfg.MarginSeconds != 30 || cfg.HTTPTimeoutSeconds != 10 {
		t.Fatalf("unexpected interval defaults: %+v", cfg)
	}
	if cfg.TelegramToken != "" || cfg.TelegramChatID != "" {
		t.Fatalf("expected empty telegram defaults, got %+v", cfg)
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("EXCHANGE_NAME", "Hyperliquid")
	t.Setenv("NETWORK", "testnet")
	t.Setenv("SYMBOL", "ETH-USDC-PERP")
	t.Setenv("DRY_RUN", "true")
	t.Setenv("PRE_RUN", "1")
	t.Setenv("DRY_RUN_MARK_PRICE", "2500")
	t.Setenv("ORDER_SIZE", "0.5")
	t.Setenv("GRID_SPACING_PERCENTAGE", "0.75")
	t.Setenv("PROFIT_PERCENTAGE", "1.25")
	t.Setenv("NUM_GRID_LEVELS", "12")
	t.Setenv("POLL_INTERVAL_SECONDS", "7")
	t.Setenv("MARGIN_CHECK_INTERVAL_SECONDS", "45")
	t.Setenv("HTTP_TIMEOUT_SECONDS", "20")
	t.Setenv("TELEGRAM_TOKEN", " tok ")
	t.Setenv("TELEGRAM_CHAT_ID", " chat ")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig overrides: %v", err)
	}
	if cfg.ExchangeName != "Hyperliquid" || cfg.Network != "testnet" || cfg.Symbol != "ETH-USDC-PERP" {
		t.Fatalf("identity override failed: %+v", cfg)
	}
	if !cfg.DryRun || !cfg.PreRun {
		t.Fatalf("bool override failed: %+v", cfg)
	}
	if cfg.DryRunMarkPrice != 2500 || cfg.OrderSize != 0.5 || cfg.GridSpacing != 0.75 || cfg.ProfitPct != 1.25 {
		t.Fatalf("float override failed: %+v", cfg)
	}
	if cfg.NumLevels != 12 || cfg.PollSeconds != 7 || cfg.MarginSeconds != 45 || cfg.HTTPTimeoutSeconds != 20 {
		t.Fatalf("int override failed: %+v", cfg)
	}
	if cfg.TelegramToken != "tok" || cfg.TelegramChatID != "chat" {
		t.Fatalf("telegram trimming failed: %+v", cfg)
	}
}

func TestLoadConfigInvalidValuesParseToDefaults(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("DRY_RUN", "not-a-bool")
	t.Setenv("NUM_GRID_LEVELS", "not-an-int")
	t.Setenv("ORDER_SIZE", "not-a-float")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("invalid parse should fall back to defaults: %v", err)
	}
	if cfg.DryRun || cfg.NumLevels != 5 || cfg.OrderSize != 0.01 {
		t.Fatalf("expected parse fallback to defaults, got %+v", cfg)
	}
}

func TestLoadConfigValidationErrors(t *testing.T) {
	cases := []struct {
		name string
		set  func(*testing.T)
	}{
		{"order size", func(t *testing.T) { t.Setenv("ORDER_SIZE", "0") }},
		{"negative order size", func(t *testing.T) { t.Setenv("ORDER_SIZE", "-1") }},
		{"levels", func(t *testing.T) { t.Setenv("NUM_GRID_LEVELS", "0") }},
		{"spacing zero", func(t *testing.T) { t.Setenv("GRID_SPACING_PERCENTAGE", "0") }},
		{"spacing too high", func(t *testing.T) { t.Setenv("GRID_SPACING_PERCENTAGE", "100") }},
		{"profit", func(t *testing.T) { t.Setenv("PROFIT_PERCENTAGE", "0") }},
		{"poll", func(t *testing.T) { t.Setenv("POLL_INTERVAL_SECONDS", "-1") }},
		{"margin", func(t *testing.T) { t.Setenv("MARGIN_CHECK_INTERVAL_SECONDS", "-1") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearConfigEnv(t)
			tc.set(t)
			if _, err := LoadConfig(); err == nil {
				t.Fatalf("expected validation error for %s", tc.name)
			}
		})
	}
}

func TestGetenvHelpers(t *testing.T) {
	t.Setenv("GC_STR", "  hello  ")
	if got := getenv("GC_STR", "fallback"); got != "hello" {
		t.Fatalf("getenv = %q", got)
	}
	t.Setenv("GC_STR", "   ")
	if got := getenv("GC_STR", "fallback"); got != "fallback" {
		t.Fatalf("blank getenv = %q", got)
	}

	t.Setenv("GC_BOOL", "true")
	if !getenvBool("GC_BOOL", false) {
		t.Fatal("getenvBool true")
	}
	t.Setenv("GC_BOOL", "garbage")
	if getenvBool("GC_BOOL", false) {
		t.Fatal("getenvBool garbage should fall back")
	}

	t.Setenv("GC_INT", "42")
	if got := getenvInt("GC_INT", 1); got != 42 {
		t.Fatalf("getenvInt = %d", got)
	}
	t.Setenv("GC_INT", "nope")
	if got := getenvInt("GC_INT", 1); got != 1 {
		t.Fatalf("getenvInt fallback = %d", got)
	}

	t.Setenv("GC_FLOAT", "3.5")
	if got := getenvFloat("GC_FLOAT", 1); got != 3.5 {
		t.Fatalf("getenvFloat = %v", got)
	}
	t.Setenv("GC_FLOAT", "nope")
	if got := getenvFloat("GC_FLOAT", 1); got != 1 {
		t.Fatalf("getenvFloat fallback = %v", got)
	}
}

func TestFirstNonEmpty(t *testing.T) {
	t.Setenv("GC_A", "")
	t.Setenv("GC_B", "  ")
	t.Setenv("GC_C", "winner")
	t.Setenv("GC_D", "loser")
	if got := FirstNonEmpty("GC_A", "GC_B", "GC_C", "GC_D"); got != "winner" {
		t.Fatalf("FirstNonEmpty = %q", got)
	}
	if got := FirstNonEmpty("GC_A", "GC_B"); got != "" {
		t.Fatalf("FirstNonEmpty all empty = %q", got)
	}
}

func TestLoadDotEnv(t *testing.T) {
	for _, k := range []string{"DOTENV_A", "DOTENV_EXPORTED", "DOTENV_QUOTED", "DOTENV_SINGLE", "DOTENV_EMPTY", "DOTENV_PRESET"} {
		t.Setenv(k, "")
	}
	t.Setenv("DOTENV_PRESET", "keep")

	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "# a comment\n" +
		"\n" +
		"DOTENV_A=value\n" +
		"export DOTENV_EXPORTED=\"quoted value\"\n" +
		"DOTENV_QUOTED='single value'\n" +
		"DOTENV_EMPTY=\n" +
		"NOEQUALS\n" +
		"=nokey\n" +
		"DOTENV_PRESET=overwritten\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadDotEnv(path); err != nil {
		t.Fatalf("LoadDotEnv: %v", err)
	}
	if got := os.Getenv("DOTENV_A"); got != "value" {
		t.Fatalf("DOTENV_A = %q", got)
	}
	if got := os.Getenv("DOTENV_EXPORTED"); got != "quoted value" {
		t.Fatalf("DOTENV_EXPORTED = %q", got)
	}
	if got := os.Getenv("DOTENV_QUOTED"); got != "single value" {
		t.Fatalf("DOTENV_QUOTED = %q", got)
	}
	if _, ok := os.LookupEnv("DOTENV_EMPTY"); !ok {
		t.Fatal("DOTENV_EMPTY should be set to empty")
	}
	if got := os.Getenv("DOTENV_PRESET"); got != "overwritten" {
		t.Fatalf("DOTENV_PRESET = %q", got)
	}
}

func TestLoadDotEnvMissingFileIsNotError(t *testing.T) {
	if err := LoadDotEnv(filepath.Join(t.TempDir(), "does-not-exist.env")); err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
}

func TestLoadDotEnvBadPathReturnsError(t *testing.T) {
	dir := t.TempDir()
	if err := LoadDotEnv(dir); err == nil {
		t.Fatal("opening a directory should return an error")
	}
}
