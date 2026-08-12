// Package db отвечает за открытие соединения с SQLite, применение миграций
// (схема встроена через embed) и сидинг справочников (монеты и источники).
//
// Драйвер modernc.org/sqlite — чистый Go, без CGO: сборка работает без
// C-компилятора.
package db

import (
	"context"
	_ "embed"
	"fmt"

	_ "modernc.org/sqlite"

	"github.com/jmoiron/sqlx"
)

//go:embed schema.sql
var schemaSQL string

// SeedAsset — справочник сидируемых монет: внешний coin_id, тикер и название.
type SeedAsset struct {
	CoinID string
	Symbol string
	Name   string
}

// SeedSource — справочник сидируемых источников.
type SeedSource struct {
	Slug string
	Name string
}

// DefaultAssets — 5 топ-монет по спеке (BTC, ETH, SOL, BNB, XRP).
var DefaultAssets = []SeedAsset{
	{CoinID: "bitcoin", Symbol: "BTC", Name: "Bitcoin"},
	{CoinID: "ethereum", Symbol: "ETH", Name: "Ethereum"},
	{CoinID: "solana", Symbol: "SOL", Name: "Solana"},
	{CoinID: "binancecoin", Symbol: "BNB", Name: "BNB"},
	{CoinID: "ripple", Symbol: "XRP", Name: "XRP"},
}

// DefaultSources — на старте один источник, CoinGecko (второй придёт в T4).
var DefaultSources = []SeedSource{
	{Slug: "coingecko", Name: "CoinGecko"},
}

// Open открывает SQLite по пути path, включает foreign_keys и WAL,
// применяет схему и сидирует справочники. Возвращает sqlx-соединение.
func Open(ctx context.Context, path string) (*sqlx.DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)", path)
	db, err := sqlx.ConnectContext(ctx, "sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("открытие sqlite %s: %w", path, err)
	}
	// modernc.org/sqlite: совместимый с database/sql, но на connect pragma выше
	// лучше дублировать через PRAGMA-выражения для надёжности.
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("включение foreign_keys: %w", err)
	}

	if err := Migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := Seed(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// Migrate применяет встроенную схему schema.sql (идемпотентно).
func Migrate(ctx context.Context, db *sqlx.DB) error {
	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("применение схемы: %w", err)
	}
	return nil
}

// Seed заполняет справочники монет и источников, если они пусты.
// Использует INSERT OR IGNORE, чтобы повторный запуск не падал на UNIQUE.
func Seed(ctx context.Context, db *sqlx.DB) error {
	for _, a := range DefaultAssets {
		if _, err := db.ExecContext(ctx,
			`INSERT OR IGNORE INTO assets (coin_id, symbol, name) VALUES (?, ?, ?)`,
			a.CoinID, a.Symbol, a.Name); err != nil {
			return fmt.Errorf("сидинг asset %s: %w", a.CoinID, err)
		}
	}
	for _, s := range DefaultSources {
		if _, err := db.ExecContext(ctx,
			`INSERT OR IGNORE INTO sources (slug, name) VALUES (?, ?)`,
			s.Slug, s.Name); err != nil {
			return fmt.Errorf("сидинг source %s: %w", s.Slug, err)
		}
	}
	return nil
}
