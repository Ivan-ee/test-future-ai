// Package coingecko — адаптер источника CoinGecko.
//
// Обёртка над публичным эндпоинтом /coins/markets. Сетевой код отделён от
// маппинга: Fetch дёргает API, MapMarkets переводит ответ в точки цен.
// MapMarkets — чистая функция, её поведение покрыто юнит-тестами без сети.
package coingecko

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"test-future/internal/model"
)

// SourceSlug — стабильный идентификатор источника в БД.
const SourceSlug = "coingecko"

// MarketCoin — одна запись из ответа /coins/markets (поля, что нужны нам).
// Формат чисел в JSON — float; совпадение обеспечивается encoding/json.
type MarketCoin struct {
	ID                           string  `json:"id"`         // coin_id, напр. "bitcoin"
	Symbol                       string  `json:"symbol"`     // тикер, напр. "btc"
	Name                         string  `json:"name"`
	CurrentPrice                 float64 `json:"current_price"`
	MarketCap                    float64 `json:"market_cap"`
	TotalVolume                  float64 `json:"total_volume"`
	PriceChangePercentage24H     float64 `json:"price_change_percentage_24h"` // в процентах, напр. 1.23
	LastUpdated                  string  `json:"last_updated"`                 // RFC3339
}

// Client — HTTP-клиент CoinGecko.
type Client struct {
	baseURL string
	http    *http.Client
}

// New создаёт клиент с указанным базовым URL.
func New(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// MarketsTaker абстрагирует сетевой вызов для тестируемости worker.
// Реализация по умолчанию — Client.Markets.
type MarketsTaker interface {
	Markets(ctx context.Context, coinIDs []string) ([]MarketCoin, error)
}

// Markets запрашивает /coins/markets для заданных coin_id.
func (c *Client) Markets(ctx context.Context, coinIDs []string) ([]MarketCoin, error) {
	if len(coinIDs) == 0 {
		return nil, nil
	}
	u, err := url.Parse(c.baseURL + "/coins/markets")
	if err != nil {
		return nil, fmt.Errorf("разбор URL CoinGecko: %w", err)
	}
	q := u.Query()
	q.Set("vs_currency", "usd")
	q.Set("ids", strings.Join(coinIDs, ","))
	q.Set("order", "market_cap_desc")
	q.Set("per_page", strconv.Itoa(len(coinIDs)))
	q.Set("page", "1")
	q.Set("sparkline", "false")
	q.Set("price_change_percentage", "24h")
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("формирование запроса CoinGecko: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	// Человекочитаемый User-Agent помогает free-tier API корректно отвечать.
	req.Header.Set("User-Agent", "test-future/0.1 (prototype)")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("запрос к CoinGecko: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("CoinGecko вернул %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var coins []MarketCoin
	if err := json.NewDecoder(resp.Body).Decode(&coins); err != nil {
		return nil, fmt.Errorf("декодирование ответа CoinGecko: %w", err)
	}
	return coins, nil
}

// MarketMap преобразует ответ /coins/markets в точки цен. Для каждой записи
// ищет asset по coin_id в карте ассетов и проставляет source_id.
// Записи без совпадения по coin_id пропускаются.
//
// Чистая функция: не трогает сеть/БД — покрыта юнит-тестами.
func MarketMap(coins []MarketCoin, assetsByCoinID map[string]model.Asset, sourceID int64) []model.PricePoint {
	out := make([]model.PricePoint, 0, len(coins))
	for _, c := range coins {
		asset, ok := assetsByCoinID[c.ID]
		if !ok {
			continue // источник вернул монету, которую мы не отслеживаем
		}
		ts, err := parseTime(c.LastUpdated)
		if err != nil || ts.IsZero() {
			ts = time.Now().UTC()
		}
		out = append(out, model.PricePoint{
			AssetID:   asset.ID,
			TS:        ts.UTC(),
			PriceUSD:  c.CurrentPrice,
			MarketCap: c.MarketCap,
			Volume:    c.TotalVolume,
			SourceID:  sourceID,
			// API отдаёт проценты (1.23 = +1.23%), в БД храним доли (0.0123).
			Change24H: c.PriceChangePercentage24H / 100.0,
		})
	}
	return out
}

func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	// CoinGecko отдаёт RFC3339 с миллисекундами.
	return time.Parse(time.RFC3339, s)
}
