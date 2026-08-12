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
	ID         int64
	SourceSlug string // ссылка на источник по слагу
	Status     UpdateStatus
	ItemsAdded int    // сколько новых записей добавлено
	Error      string // текст ошибки, если status == error
	StartedAt  time.Time
	FinishedAt time.Time
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

// IndicatorSnapshot — последние посчитанные технические индикаторы по монете.
// Одна строка на актив (UPSERT по asset_id при каждом цикле worker).
type IndicatorSnapshot struct {
	ID           int64
	AssetID      int64
	SourceID     int64
	RSI          float64 // RSI(14), 0..100
	ROC          float64 // ROC(10), проценты
	SMA7         float64 // SMA(7)
	SMA20        float64 // SMA(20)
	VolumeSignal float64 // отношение последнего объёма к среднему за 14д
	CalculatedAt time.Time
}

// IndicatorValue — значение индикатора + человекочитаемая интерпретация.
type IndicatorValue struct {
	Value          float64 `json:"value"`
	Interpretation string  `json:"interpretation"`
}

// IndicatorsView — набор индикаторов для детального эндпоинта.
// CalculatedAt nil, если индикаторы ещё не посчитаны (null в JSON).
type IndicatorsView struct {
	RSI          IndicatorValue `json:"rsi"`
	ROC          IndicatorValue `json:"roc"`
	SMA7         IndicatorValue `json:"sma_7"`
	SMA20        IndicatorValue `json:"sma_20"`
	VolumeSignal IndicatorValue `json:"volume_signal"`
	CalculatedAt *time.Time     `json:"calculated_at"`
}

// AssetDetail — DTO для эндпоинта GET /api/assets/{id}: актив с ценой и
// последними значениями технических индикаторов. Встраивает AssetPrice, добавляя
// блок indicators.
type AssetDetail struct {
	AssetPrice
	Indicators IndicatorsView `json:"indicators"`
}

// ForecastStatus — статус прогноза: active (актуальный), superseded (заменён
// более свежим — история остаётся для аудита формулы).
type ForecastStatus string

const (
	ForecastStatusActive     ForecastStatus = "active"
	ForecastStatusSuperseded ForecastStatus = "superseded"
)

// Forecast — запись прогноза «вверх/вниз за 24ч» по активу на момент времени.
// Одна строка на цикл пересчёта; статус active у последнего, остальные —
// superseded (история для аудита формулы).
type Forecast struct {
	ID           int64
	AssetID      int64
	CreatedAt    time.Time
	HorizonHours int            // горизонт прогноза (24)
	Direction    string         // "up" | "down"
	Confidence   float64        // [0.5, 1.0]
	RiskNote     string         // короткая заметка о риске
	ArgumentText string         // детерминированная текстовая аргументация
	RawScore     float64        // Σ(signal × adjusted_weight)
	Status       ForecastStatus // active | superseded
}

// ForecastFactor — декомпозиция вклада одного фактора в прогноз. Хранится
// отдельно для прозрачности: какой сигнал, какой вес, какой вклад.
type ForecastFactor struct {
	ID             int64
	ForecastID     int64
	Name           string  // "rsi" | "momentum" | "volume"
	Signal         float64 // [-1..1]
	BaseWeight     float64 // исходный вес до нормировки
	AdjustedWeight float64 // нормированный вес (сумма по факторам = 1.0)
	Contribution   float64 // signal × adjusted_weight
	Detail         string  // описание значений, использованных для сигнала
}

// ForecastFactorView — DTO факторa в ответе API.
type ForecastFactorView struct {
	Name           string  `json:"name"`
	Signal         float64 `json:"signal"`
	BaseWeight     float64 `json:"base_weight"`
	AdjustedWeight float64 `json:"adjusted_weight"`
	Contribution   float64 `json:"contribution"`
	Detail         string  `json:"detail"`
}

// ForecastDataView — «использованные данные»: сырые значения, из которых
// считались сигналы факторов. Помогает пользователю понять, откуда прогноз.
type ForecastDataView struct {
	PriceUSD     float64    `json:"price_usd"`
	RSI          float64    `json:"rsi"`
	ROC          float64    `json:"roc"`
	SMA7         float64    `json:"sma_7"`
	SMA20        float64    `json:"sma_20"`
	VolumeSignal float64    `json:"volume_signal"`
	Change24H    float64    `json:"change_24h"`
	CalculatedAt *time.Time `json:"calculated_at"` // время расчёта индикаторов
}

// NewsItem — новость из внешнего источника (CoinPaprika, RSS). Может быть
// связана с конкретной монетой (AssetID != nil) или быть общей (AssetID == nil,
// например лента CoinDesk). Сентимент проставляется позже через OpenAI.
type NewsItem struct {
	ID               int64
	AssetID          *int64  // nullable: связь с монетой, если возможно
	SourceID         int64
	ExternalID       string  // идентификатор новости у источника (для дедупа)
	Title            string
	Body             string
	Link             string
	PublishedAt      time.Time
	SentimentScore   *float64 // nullable: проставляется сентимент-сервисом
	SentimentSummary *string  // nullable: короткое резюме сентимента
	InsertedAt       time.Time
}

// NewsItemView — DTO новости для API: только то, что нужно карточке прогноза.
type NewsItemView struct {
	Title            string   `json:"title"`
	Link             string   `json:"link"`
	PublishedAt      time.Time `json:"published_at"`
	SentimentScore   *float64 `json:"sentiment_score"`   // null, если не оценён
	SentimentSummary *string  `json:"sentiment_summary"` // null, если не оценён
}

// ForecastView — DTO для эндпоинта GET /api/forecasts/:asset: полный прогноз
// с декомпозицией по факторам, использованными данными и последними новостями.
type ForecastView struct {
	AssetID      int64                `json:"asset_id"`
	Symbol       string               `json:"symbol"`
	Name         string               `json:"name"`
	CreatedAt    time.Time            `json:"created_at"`
	HorizonHours int                  `json:"horizon_hours"`
	Direction    string               `json:"direction"`
	Confidence   float64              `json:"confidence"`
	RiskNote     string               `json:"risk_note"`
	ArgumentText string               `json:"argument_text"`
	RawScore     float64              `json:"raw_score"`
	Factors      []ForecastFactorView `json:"factors"`
	Data         ForecastDataView     `json:"data"`
	News         []NewsItemView       `json:"news"`
}

// ForecastSummary — краткий прогноз для списка и главной страницы:
// направление + уверенность + когда посчитан.
type ForecastSummary struct {
	AssetID    int64     `json:"asset_id"`
	Symbol     string    `json:"symbol"`
	Name       string    `json:"name"`
	Direction  string    `json:"direction"`
	Confidence float64   `json:"confidence"`
	CreatedAt  time.Time `json:"created_at"`
}
