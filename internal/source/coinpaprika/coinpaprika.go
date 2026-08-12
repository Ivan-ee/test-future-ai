// Package coinpaprika — адаптер источника новостей CoinPaprika.
//
// Обёртка над публичным эндпоинтом /v1/news. Сетевой код отделён от маппинга:
// News дёргает API, NewsMap переводит ответ в доменные model.NewsItem.
// NewsMap — чистая функция, её поведение покрыто юнит-тестами без сети.
package coinpaprika

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
const SourceSlug = "coinpaprika"

// RawNews — одна запись из ответа /v1/news (поля, что нужны нам).
type RawNews struct {
	ID           string   `json:"id"`            // внешний идентификатор новости
	Title        string   `json:"title"`         // заголовок
	Summary      string   `json:"summary"`       // lead-текст (анонс)
	URL          string   `json:"url"`           // ссылка на полный текст
	PublishedAt  string   `json:"date"`          // RFC3339 (поле "date" в API CoinPaprika)
	RelatedCoins []string `json:"related_coins"` // слаги монет, напр. "btc-bitcoin"
}

// Client — HTTP-клиент CoinPaprika.
type Client struct {
	baseURL string
	http    *http.Client
}

// New создаёт клиент с указанным базовым URL.
func New(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// NewsTaker абстрагирует сетевой вызов для тестируемости worker.
type NewsTaker interface {
	News(ctx context.Context, coinSlugs []string, limit int) ([]RawNews, error)
}

// News запрашивает /v1/news?coins=btc-bitcoin,eth-ethereum,...&limit=N.
func (c *Client) News(ctx context.Context, coinSlugs []string, limit int) ([]RawNews, error) {
	if len(coinSlugs) == 0 {
		return nil, nil
	}
	u, err := url.Parse(c.baseURL + "/news")
	if err != nil {
		return nil, fmt.Errorf("разбор URL CoinPaprika: %w", err)
	}
	q := u.Query()
	q.Set("coins", strings.Join(coinSlugs, ","))
	q.Set("limit", strconv.Itoa(limit))
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("формирование запроса CoinPaprika: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "test-future/0.1 (prototype)")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("запрос к CoinPaprika: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("CoinPaprika вернул %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var news []RawNews
	if err := json.NewDecoder(resp.Body).Decode(&news); err != nil {
		return nil, fmt.Errorf("декодирование ответа CoinPaprika: %w", err)
	}
	return news, nil
}

// coinSlugToCoinID — соответствие слагов CoinPaprika → coin_id CoinGecko
// (идентификаторы в нашей БД совпадают с CoinGecko). Единый источник правды:
// используется и для генерации списка запрашиваемых слагов (SlugsForCoinIDs),
// и для маппинга ответа в NewsMap.
var coinSlugToCoinID = map[string]string{
	"btc-bitcoin":      "bitcoin",
	"eth-ethereum":     "ethereum",
	"sol-solana":       "solana",
	"bnb-binance-coin": "binancecoin",
	"xrp-xrp":          "ripple",
}

// coinIDToSlug — обратное отображение, строится один раз при инициализации.
var coinIDToSlug = func() map[string]string {
	m := make(map[string]string, len(coinSlugToCoinID))
	for slug, coinID := range coinSlugToCoinID {
		m[coinID] = slug
	}
	return m
}()

// SlugsForCoinIDs возвращает слаги CoinPaprika для заданных coin_id CoinGecko.
// coin_id без известного слага пропускаются. Используется worker для формирования
// запроса новостей — единая точка правды для списка запрашиваемых слагов.
func SlugsForCoinIDs(coinIDs []string) []string {
	out := make([]string, 0, len(coinIDs))
	for _, id := range coinIDs {
		if slug, ok := coinIDToSlug[id]; ok {
			out = append(out, slug)
		}
	}
	return out
}

// NewsMap преобразует ответ /v1/news в доменные NewsItem. Для каждой записи
// пытается связать новость с монетой по слагу из RelatedCoins. Если совпадений
// нет — новость сохраняется без привязки к монете (asset_id=nil).
//
// Чистая функция: не трогает сеть/БД — покрыта юнит-тестами.
func NewsMap(raw []RawNews, assetsByCoinID map[string]model.Asset, sourceID int64) []model.NewsItem {
	out := make([]model.NewsItem, 0, len(raw))
	for _, r := range raw {
		ts, err := parseTime(r.PublishedAt)
		if err != nil || ts.IsZero() {
			ts = time.Now().UTC()
		}

		var assetID *int64
		for _, slug := range r.RelatedCoins {
			coinID, ok := coinSlugToCoinID[slug]
			if !ok {
				continue
			}
			asset, found := assetsByCoinID[coinID]
			if found {
				aid := asset.ID
				assetID = &aid
				break // первая совпадающая монета
			}
		}

		out = append(out, model.NewsItem{
			AssetID:     assetID,
			SourceID:    sourceID,
			ExternalID:  r.ID,
			Title:       r.Title,
			Body:        r.Summary,
			Link:        r.URL,
			PublishedAt: ts.UTC(),
		})
	}
	return out
}

func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	// CoinPaprika отдаёт RFC3339.
	return time.Parse(time.RFC3339, s)
}
