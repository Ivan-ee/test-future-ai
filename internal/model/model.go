// Package model содержит доменные типы проекта test-future.
//
// Здесь описаны сущности предметной области: активы (монеты), источники данных,
// точки цен и записи истории обновлений. Типы не зависят от слоя хранения и
// транспорта — это чистые структуры данных, которыми обмениваются слои
// source → storage → server.
package model

import "time"

// Asset — отслеживаемая криптовалюта.
type Asset struct {
	ID        int64     // внутренний идентификатор
	CoinID    string    // внешний идентификатор источника, например "bitcoin"
	Symbol    string    // тикер, например "BTC"
	Name      string    // человекочитаемое название, например "Bitcoin"
	CreatedAt time.Time // момент заведения в систему
}

// Source — реестр источников данных.
type Source struct {
	ID        int64
	Slug      string // стабильный слаг, например "coingecko"
	Name      string
	CreatedAt time.Time
}

// PricePoint — точка цены актива в момент времени от конкретного источника.
type PricePoint struct {
	ID         int64
	AssetID    int64
	TS         time.Time // момент наблюдения цены
	PriceUSD   float64
	MarketCap  float64
	Volume     float64
	SourceID   int64
	Change24H  float64 // изменение за 24ч в долях (0.01 = +1%)
	InsertedAt time.Time
}

// UpdateStatus — статус запуска обновления источника.
type UpdateStatus string

const (
	UpdateStatusOK    UpdateStatus = "ok"
	UpdateStatusError UpdateStatus = "error"
)

// UpdateLog — запись истории одного цикла обновления источника.
type UpdateLog struct {
	ID          int64
	SourceSlug  string    // ссылка на источник по слагу
	Status      UpdateStatus
	ItemsAdded  int       // сколько новых записей добавлено
	Error       string    // текст ошибки, если status == error
	StartedAt   time.Time
	FinishedAt  time.Time
}

// AssetPrice — DTO для эндпоинта GET /api/assets: актив с последней ценой.
type AssetPrice struct {
	AssetID     int64     `json:"asset_id"`
	CoinID      string    `json:"coin_id"`
	Symbol      string    `json:"symbol"`
	Name        string    `json:"name"`
	PriceUSD    float64   `json:"price_usd"`
	MarketCap   float64   `json:"market_cap"`
	Volume      float64   `json:"volume"`
	Change24H   float64   `json:"change_24h"`
	LastUpdated time.Time `json:"last_updated"`
}
