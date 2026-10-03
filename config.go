package gridcore

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config holds only the venue-agnostic settings the grid engine needs.
// Venue-specific settings (base URLs, chain IDs, API keys, market IDs, order
// expiry, auth/retry tuning) belong to the per-venue adapter, which loads them
// itself and passes this struct to New.
type Config struct {
	ExchangeName    string
	Network         string
	Symbol          string
	DryRun          bool
	PreRun          bool
	DryRunMarkPrice float64

	OrderSize   float64
	GridSpacing float64
	ProfitPct   float64
	NumLevels   int

	PollSeconds        int
	MarginSeconds      int
	HTTPTimeoutSeconds int

	TelegramToken  string
	TelegramChatID string
}

// LoadConfig reads the venue-agnostic grid settings from the environment,
// applying the same defaults the reference implementation shipped with.
func LoadConfig() (Config, error) {
	cfg := Config{
		ExchangeName:       getenv("EXCHANGE_NAME", "Lighter"),
		Network:            getenv("NETWORK", "mainnet"),
		Symbol:             getenv("SYMBOL", "BTC-USDT-PERP"),
		DryRun:             getenvBool("DRY_RUN", false),
		PreRun:             getenvBool("PRE_RUN", false),
		DryRunMarkPrice:    getenvFloat("DRY_RUN_MARK_PRICE", 100000),
		OrderSize:          getenvFloat("ORDER_SIZE", 0.01),
		GridSpacing:        getenvFloat("GRID_SPACING_PERCENTAGE", 0.2),
		ProfitPct:          getenvFloat("PROFIT_PERCENTAGE", 0.2),
		NumLevels:          getenvInt("NUM_GRID_LEVELS", 5),
		PollSeconds:        getenvInt("POLL_INTERVAL_SECONDS", 3),
		MarginSeconds:      getenvInt("MARGIN_CHECK_INTERVAL_SECONDS", 30),
		HTTPTimeoutSeconds: getenvInt("HTTP_TIMEOUT_SECONDS", 10),
		TelegramToken:      strings.TrimSpace(os.Getenv("TELEGRAM_TOKEN")),
		TelegramChatID:     strings.TrimSpace(os.Getenv("TELEGRAM_CHAT_ID")),
	}
	if cfg.OrderSize <= 0 {
		return Config{}, fmt.Errorf("ORDER_SIZE must be > 0")
	}
	if cfg.NumLevels < 1 {
		return Config{}, fmt.Errorf("NUM_GRID_LEVELS must be >= 1")
	}
	if cfg.GridSpacing <= 0 || cfg.GridSpacing >= 100 {
		return Config{}, fmt.Errorf("GRID_SPACING_PERCENTAGE must be within (0,100)")
	}
	if cfg.ProfitPct <= 0 {
		return Config{}, fmt.Errorf("PROFIT_PERCENTAGE must be > 0")
	}
	if cfg.PollSeconds < 0 {
		return Config{}, fmt.Errorf("POLL_INTERVAL_SECONDS must be >= 0")
	}
	if cfg.MarginSeconds < 0 {
		return Config{}, fmt.Errorf("MARGIN_CHECK_INTERVAL_SECONDS must be >= 0")
	}
	return cfg, nil
}

func getenv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func getenvBool(key string, fallback bool) bool {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		b, err := strconv.ParseBool(v)
		if err == nil {
			return b
		}
	}
	return fallback
}

func getenvInt(key string, fallback int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		n, err := strconv.Atoi(v)
		if err == nil {
			return n
		}
	}
	return fallback
}

func getenvFloat(key string, fallback float64) float64 {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err == nil {
			return f
		}
	}
	return fallback
}

// FirstNonEmpty returns the first non-empty environment variable among keys.
func FirstNonEmpty(keys ...string) string {
	for _, key := range keys {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return ""
}

// LoadDotEnv loads KEY=VALUE pairs from path into the process environment,
// without overriding values that are already set. A missing file is not an error.
func LoadDotEnv(path string) error {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" {
			continue
		}
		value = strings.Trim(value, `"'`)
		_ = os.Setenv(key, value)
	}
	return scanner.Err()
}
