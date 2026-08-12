// Package rss — адаптер источников новостей через RSS-ленты.
//
// Через mmcdole/gofeed парсит ленты CoinDesk и Cointelegraph. RSS-новости —
// общие (не привязаны к конкретной монете), поэтому asset_id=nil. Дедуп
// обеспечивается репозиторием по UNIQUE(source_id, external_id); external_id
// для RSS — это Link новости (URL), он уникален в рамках ленты.
package rss

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"

	"test-future/internal/model"
)

// Slugs — стабильные идентификаторы RSS-источников в БД.
const (
	SlugCoindesk      = "rss-coindesk"
	SlugCointelegraph = "rss-cointelegraph"
)

// FeedURLs — URL лент по слагу источника.
var FeedURLs = map[string]string{
	SlugCoindesk:      "https://www.coindesk.com/arc/outboundfeeds/rss/",
	SlugCointelegraph: "https://cointelegraph.com/rss",
}

// RawNews — нормализованный элемент ленты (общий для источников).
type RawNews struct {
	ExternalID  string // Link (URL) — уникален в рамках ленты
	Title       string
	Summary     string
	Link        string
	PublishedAt time.Time
}

// Fetcher парсит RSS-ленту через gofeed.
type Fetcher struct {
	parser *gofeed.Parser
}

// New создаёт Fetcher с gofeed-парсером по умолчанию.
func New() *Fetcher {
	return &Fetcher{parser: gofeed.NewParser()}
}

// NewsTaker абстрагирует сетевой вызов для тестируемости worker.
type NewsTaker interface {
	News(ctx context.Context, sourceSlug string) ([]RawNews, error)
}

// News парсит ленту по слагу источника. Если слаг неизвестен — ошибка.
func (f *Fetcher) News(ctx context.Context, sourceSlug string) ([]RawNews, error) {
	feedURL, ok := FeedURLs[sourceSlug]
	if !ok {
		return nil, fmt.Errorf("неизвестный RSS-источник: %s", sourceSlug)
	}

	feed, err := f.parser.ParseURLWithContext(feedURL, ctx)
	if err != nil {
		return nil, fmt.Errorf("парсинг RSS %s: %w", sourceSlug, err)
	}

	out := make([]RawNews, 0, len(feed.Items))
	for _, item := range feed.Items {
		link := item.Link
		if link == "" && len(item.Links) > 0 {
			link = item.Links[0]
		}
		if link == "" {
			continue // без ссылки нет дедуп-ключа
		}
		summary := item.Description
		if summary == "" && item.Content != "" {
			summary = item.Content
		}
		var pub time.Time
		if item.PublishedParsed != nil {
			pub = item.PublishedParsed.UTC()
		} else if item.UpdatedParsed != nil {
			pub = item.UpdatedParsed.UTC()
		} else {
			pub = time.Now().UTC()
		}
		out = append(out, RawNews{
			ExternalID:  link,
			Title:       strings.TrimSpace(item.Title),
			Summary:     strings.TrimSpace(summary),
			Link:        link,
			PublishedAt: pub,
		})
	}
	return out, nil
}

// NewsMap преобразует нормализованные элементы ленты в доменные NewsItem.
// assetID всегда nil — RSS-ленты общие, не привязаны к конкретной монете.
//
// Чистая функция: не трогает сеть/БД — покрыта юнит-тестами.
func NewsMap(raw []RawNews, sourceID int64) []model.NewsItem {
	out := make([]model.NewsItem, 0, len(raw))
	for _, r := range raw {
		out = append(out, model.NewsItem{
			AssetID:     nil,
			SourceID:    sourceID,
			ExternalID:  r.ExternalID,
			Title:       r.Title,
			Body:        r.Summary,
			Link:        r.Link,
			PublishedAt: r.PublishedAt.UTC(),
		})
	}
	return out
}
