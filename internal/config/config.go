// Package config читает конфигурацию приложения из переменных окружения.
//
// Переменные можно задавать как в системном окружении, так и в файле .env
// (загружается через godotenv, если файл существует). Все значения имеют
// безопасные значения по умолчанию, чтобы `make dev` работал из коробки.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/joho/godotenv"
)

// Config — конфигурация приложения.
type Config struct {
	OpenAIAPIKey     string
	CoinGeckoBaseURL string
	BackendPort      int
	FrontendPort     int
	DBPath           string
	FetchIntervalMin int // период опроса источников, минут (по умолчанию 10)
}

// Load загружает конфигурацию: сначала пробует .env в корне репозитория, затем
// считывает переменные окружения.
func Load() (Config, error) {
	// .env необязателен — системные переменные тоже работают.
	_ = godotenv.Load()

	cfg := Config{
		OpenAIAPIKey:     getenv("OPENAI_API_KEY", ""),
		CoinGeckoBaseURL: getenv("COINGECKO_BASE_URL", "https://api.coingecko.com/api/v3"),
		DBPath:           getenv("DB_PATH", "data/testfuture.db"),
		FetchIntervalMin: getenvInt("FETCH_INTERVAL_MIN", 10),
	}
	cfg.BackendPort = getenvInt("BACKEND_PORT", 8081)
	cfg.FrontendPort = getenvInt("FRONTEND_PORT", 3001)

	if cfg.CoinGeckoBaseURL == "" {
		return Config{}, fmt.Errorf("COINGECKO_BASE_URL не должен быть пустым")
	}
	if cfg.DBPath == "" {
		return Config{}, fmt.Errorf("DB_PATH не должен быть пустым")
	}
	return cfg, nil
}

// BackendAddr возвращает адрес вида ":8081" для http.Server.
func (c Config) BackendAddr() string { return fmt.Sprintf(":%d", c.BackendPort) }

// FrontendURL возвращает базовый URL фронтенда для настройки CORS.
func (c Config) FrontendURL() string {
	return fmt.Sprintf("http://localhost:%d", c.FrontendPort)
}

// EnsureDBDir создаёт родительский каталог файла БД, если его нет.
func (c Config) EnsureDBDir() error {
	dir := filepath.Dir(c.DBPath)
	if dir == "" || dir == "." {
		return nil
	}
	return os.MkdirAll(dir, 0o755)
}

func getenv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func getenvInt(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
