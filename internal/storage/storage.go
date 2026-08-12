// Package storage — репозитории поверх sqlx для доменных сущностей.
//
// Репозитории инкапсулируют SQL и трансляцию строк в доменные типы из model.
// Слой source пишет сюда сырые наблюдения; слой server читает отсюда
// агрегированные DTO для API.
package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"

	"test-future/internal/model"
)

// Assets — репозиторий справочника монет.
type Assets struct{ db *sqlx.DB }

func NewAssets(db *sqlx.DB) *Assets { return &Assets{db: db} }

// All возвращает все зарегистрированные монеты.
func (r *Assets) All(ctx context.Context) ([]model.Asset, error) {
	var rows []assetRow
	const q = `SELECT id, coin_id, symbol, name, created_at FROM assets ORDER BY id`
	if err := r.db.SelectContext(ctx, &rows, q); err != nil {
		return nil, fmt.Errorf("выборка assets: %w", err)
	}
	out := make([]model.Asset, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.toModel())
	}
	return out, nil
}

// MapByCoinID возвращает map coin_id → Asset для быстрого сопоставления с
// ответами внешнего API.
func (r *Assets) MapByCoinID(ctx context.Context) (map[string]model.Asset, error) {
	assets, err := r.All(ctx)
	if err != nil {
		return nil, err
	}
	m := make(map[string]model.Asset, len(assets))
	for _, a := range assets {
		m[a.CoinID] = a
	}
	return m, nil
}

// Sources — репозиторий реестра источников.
type Sources struct{ db *sqlx.DB }

func NewSources(db *sqlx.DB) *Sources { return &Sources{db: db} }

// BySlug возвращает источник по слагу.
func (r *Sources) BySlug(ctx context.Context, slug string) (model.Source, error) {
	var row sourceRow
	const q = `SELECT id, slug, name, created_at FROM sources WHERE slug = ?`
	if err := r.db.GetContext(ctx, &row, q, slug); err != nil {
		return model.Source{}, fmt.Errorf("выборка source %s: %w", slug, err)
	}
	return row.toModel(), nil
}

// PricePoints — репозиторий точек цен.
type PricePoints struct{ db *sqlx.DB }

func NewPricePoints(db *sqlx.DB) *PricePoints { return &PricePoints{db: db} }

// InsertOne добавляет одну точку цены. Дедуп обеспечивает уникальный индекс
// (asset_id, ts, source_id): при конфликте запись игнорируется, возвращается
// false (не новая). Возвращает true, если строка была добавлена.
func (r *PricePoints) InsertOne(ctx context.Context, p model.PricePoint) (bool, error) {
	const q = `
INSERT OR IGNORE INTO price_points
    (asset_id, ts, price_usd, market_cap, volume, source_id, change_24h)
VALUES (?, ?, ?, ?, ?, ?, ?)`
	res, err := r.db.ExecContext(ctx, q,
		p.AssetID, p.TS.UTC(), p.PriceUSD, p.MarketCap, p.Volume, p.SourceID, p.Change24H)
	if err != nil {
		return false, fmt.Errorf("вставка price_point: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("rows affected: %w", err)
	}
	return n > 0, nil
}

// LatestByAsset возвращает активы с их последней ценой (DTO для API).
// Берёт самую свежую точку цены по каждому активу; при совпадении ts у
// нескольких источников — детерминированно выбирает запись с наименьшим
// source_id (чтобы на один актив было ровно одно значение).
func (r *PricePoints) LatestByAsset(ctx context.Context) ([]model.AssetPrice, error) {
	const q = `
SELECT
    a.id            AS asset_id,
    a.coin_id       AS coin_id,
    a.symbol        AS symbol,
    a.name          AS name,
    pp.price_usd    AS price_usd,
    pp.market_cap   AS market_cap,
    pp.volume       AS volume,
    pp.change_24h   AS change_24h,
    pp.ts           AS last_updated
FROM assets a
LEFT JOIN price_points pp ON pp.id = (
    SELECT p.id FROM price_points p
    WHERE p.asset_id = a.id
    ORDER BY p.ts DESC, p.source_id ASC
    LIMIT 1
)
ORDER BY a.id`
	var rows []assetPriceRow
	if err := r.db.SelectContext(ctx, &rows, q); err != nil {
		return nil, fmt.Errorf("выборка последних цен: %w", err)
	}
	out := make([]model.AssetPrice, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.toModel())
	}
	return out, nil
}

// LatestByAssetID возвращает DTO последней цены для одного актива по его id.
// Используется детальным эндпоинтом GET /api/assets/{id}. Если актив не найден
// среди зарегистрированных (даже без точек цен) — sql.ErrNoRows; вызывающий
// трактует это как 404. Актив без цен вернётся с нулевой ценой и ошибкой nil.
func (r *PricePoints) LatestByAssetID(ctx context.Context, assetID int64) (model.AssetPrice, error) {
	items, err := r.LatestByAsset(ctx)
	if err != nil {
		return model.AssetPrice{}, err
	}
	for _, ap := range items {
		if ap.AssetID == assetID {
			return ap, nil
		}
	}
	return model.AssetPrice{}, sql.ErrNoRows
}

// LastClosesByAsset возвращает последние n цен закрытия актива по возрастанию ts
// (старые → новые) — для расчёта индикаторов (RSI/ROC/SMA).
func (r *PricePoints) LastClosesByAsset(ctx context.Context, assetID int64, n int) ([]float64, error) {
	rows, err := r.lastDedupedByAsset(ctx, assetID, n)
	if err != nil {
		return nil, err
	}
	out := make([]float64, len(rows))
	for i, row := range rows {
		out[i] = row.PriceUSD
	}
	return out, nil
}

// LastVolumesByAsset возвращает последние n объёмов актива по возрастанию ts —
// для расчёта VolumeSignal.
func (r *PricePoints) LastVolumesByAsset(ctx context.Context, assetID int64, n int) ([]float64, error) {
	rows, err := r.lastDedupedByAsset(ctx, assetID, n)
	if err != nil {
		return nil, err
	}
	out := make([]float64, len(rows))
	for i, row := range rows {
		out[i] = row.Volume
	}
	return out, nil
}

// dedupedRow — одна точка ряда (дедуплицированная по ts) для расчёта индикаторов.
type dedupedRow struct {
	PriceUSD float64 `db:"price_usd"`
	Volume   float64 `db:"volume"`
}

// lastDedupedByAsset — общий хелпер: последние n точек (цена + объём) актива
// по возрастанию ts с дедупом по моменту (детерминированно минимальный source_id).
// Используется и для closes, и для volumes, чтобы логика дедупа была в одном месте.
func (r *PricePoints) lastDedupedByAsset(ctx context.Context, assetID int64, n int) ([]dedupedRow, error) {
	const q = `
SELECT pp.price_usd AS price_usd, pp.volume AS volume FROM (
    SELECT ts, MIN(source_id) AS sid
    FROM price_points
    WHERE asset_id = ?
    GROUP BY ts
    ORDER BY ts DESC
    LIMIT ?
) d
JOIN price_points pp ON pp.asset_id = ? AND pp.ts = d.ts AND pp.source_id = d.sid
ORDER BY d.ts ASC`
	var rows []dedupedRow
	if err := r.db.SelectContext(ctx, &rows, q, assetID, n, assetID); err != nil {
		return nil, fmt.Errorf("выборка ряда для asset %d: %w", assetID, err)
	}
	return rows, nil
}

// UpdateLog — репозиторий журнала обновлений.
type UpdateLog struct{ db *sqlx.DB }

func NewUpdateLog(db *sqlx.DB) *UpdateLog { return &UpdateLog{db: db} }

// Record вставляет запись о результате цикла обновления источника.
func (r *UpdateLog) Record(ctx context.Context, entry model.UpdateLog) error {
	const q = `
INSERT INTO update_log (source_slug, status, items_added, error, started_at, finished_at)
VALUES (?, ?, ?, ?, ?, ?)`
	_, err := r.db.ExecContext(ctx, q,
		entry.SourceSlug, string(entry.Status), entry.ItemsAdded, entry.Error,
		entry.StartedAt.UTC(), entry.FinishedAt.UTC())
	if err != nil {
		return fmt.Errorf("вставка update_log: %w", err)
	}
	return nil
}

// IndicatorSnapshots — репозиторий посчитанных индикаторов.
type IndicatorSnapshots struct{ db *sqlx.DB }

func NewIndicatorSnapshots(db *sqlx.DB) *IndicatorSnapshots {
	return &IndicatorSnapshots{db: db}
}

// Upsert сохраняет последние значения индикаторов по активу. При конфликте
// по asset_id — обновляет все поля (одна актуальная строка на монету).
func (r *IndicatorSnapshots) Upsert(ctx context.Context, s model.IndicatorSnapshot) error {
	const q = `
INSERT INTO indicator_snapshots
    (asset_id, source_id, rsi, roc, sma_7, sma_20, volume_signal, calculated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(asset_id) DO UPDATE SET
    source_id      = excluded.source_id,
    rsi            = excluded.rsi,
    roc            = excluded.roc,
    sma_7          = excluded.sma_7,
    sma_20         = excluded.sma_20,
    volume_signal  = excluded.volume_signal,
    calculated_at  = excluded.calculated_at`
	_, err := r.db.ExecContext(ctx, q,
		s.AssetID, s.SourceID, s.RSI, s.ROC, s.SMA7, s.SMA20, s.VolumeSignal, s.CalculatedAt.UTC())
	if err != nil {
		return fmt.Errorf("upsert indicator_snapshot для asset %d: %w", s.AssetID, err)
	}
	return nil
}

// ByAsset возвращает последний снапшот индикаторов по активу. Если данных ещё
// нет — sql.ErrNoRows (детальный эндпоинт трактует как «индикаторы не готовы»).
func (r *IndicatorSnapshots) ByAsset(ctx context.Context, assetID int64) (model.IndicatorSnapshot, error) {
	var row indicatorSnapshotRow
	const q = `
SELECT id, asset_id, source_id, rsi, roc, sma_7, sma_20, volume_signal, calculated_at
FROM indicator_snapshots WHERE asset_id = ?`
	if err := r.db.GetContext(ctx, &row, q, assetID); err != nil {
		return model.IndicatorSnapshot{}, fmt.Errorf("выборка indicator_snapshot для asset %d: %w", assetID, err)
	}
	return row.toModel(), nil
}

// NewsItems — репозиторий новостей из внешних источников.
type NewsItems struct{ db *sqlx.DB }

func NewNewsItems(db *sqlx.DB) *NewsItems { return &NewsItems{db: db} }

// InsertMany добавляет новости батчем с дедупом по UNIQUE(source_id, external_id).
// Возвращает количество фактически добавленных строк.
func (r *NewsItems) InsertMany(ctx context.Context, items []model.NewsItem) (int, error) {
	added := 0
	for _, n := range items {
		var assetID any
		if n.AssetID != nil {
			assetID = *n.AssetID
		}
		res, err := r.db.ExecContext(ctx, `
INSERT OR IGNORE INTO news_items
    (asset_id, source_id, external_id, title, body, link, published_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
			assetID, n.SourceID, n.ExternalID, n.Title, n.Body, n.Link, n.PublishedAt.UTC())
		if err != nil {
			return added, fmt.Errorf("вставка news_item %s: %w", n.ExternalID, err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			added++
		}
	}
	return added, nil
}

// Unscored возвращает новости без сентимента (sentiment_score IS NULL), лимит —
// для батчевой обработки сентимент-сервисом.
func (r *NewsItems) Unscored(ctx context.Context, limit int) ([]model.NewsItem, error) {
	var rows []newsItemRow
	const q = `
SELECT id, asset_id, source_id, external_id, title, body, link, published_at,
       sentiment_score, sentiment_summary, inserted_at
FROM news_items WHERE sentiment_score IS NULL
ORDER BY inserted_at ASC LIMIT ?`
	if err := r.db.SelectContext(ctx, &rows, q, limit); err != nil {
		return nil, fmt.Errorf("выборка неотсентиченных новостей: %w", err)
	}
	out := make([]model.NewsItem, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.toModel())
	}
	return out, nil
}

// SetSentiment проставляет сентимент одной новости. Score зажимается в [-1..1].
func (r *NewsItems) SetSentiment(ctx context.Context, id int64, score float64, summary string) error {
	score = clampScore(score)
	var sumVal any
	if summary != "" {
		sumVal = summary
	}
	const q = `UPDATE news_items SET sentiment_score = ?, sentiment_summary = ? WHERE id = ?`
	if _, err := r.db.ExecContext(ctx, q, score, sumVal, id); err != nil {
		return fmt.Errorf("обновление сентимента news_item %d: %w", id, err)
	}
	return nil
}

// RecentByAsset возвращает последние новости по монете за период (с фильтром
// по published_at >= since), отсортированные от свежих к старым. Лимит — топ-N.
func (r *NewsItems) RecentByAsset(ctx context.Context, assetID int64, since time.Time, limit int) ([]model.NewsItem, error) {
	var rows []newsItemRow
	const q = `
SELECT id, asset_id, source_id, external_id, title, body, link, published_at,
       sentiment_score, sentiment_summary, inserted_at
FROM news_items WHERE asset_id = ? AND published_at >= ?
ORDER BY published_at DESC LIMIT ?`
	if err := r.db.SelectContext(ctx, &rows, q, assetID, since.UTC(), limit); err != nil {
		return nil, fmt.Errorf("выборка новостей по asset %d: %w", assetID, err)
	}
	out := make([]model.NewsItem, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.toModel())
	}
	return out, nil
}

// AvgSentimentByAsset возвращает средний сентимент новостей по монете за период.
// Возвращает nil, если нет новостей с проставленным сентиментом.
func (r *NewsItems) AvgSentimentByAsset(ctx context.Context, assetID int64, since time.Time) (*float64, error) {
	const q = `
SELECT AVG(sentiment_score) FROM news_items
WHERE asset_id = ? AND published_at >= ? AND sentiment_score IS NOT NULL`
	var avg sql.NullFloat64
	if err := r.db.GetContext(ctx, &avg, q, assetID, since.UTC()); err != nil {
		return nil, fmt.Errorf("средний сентимент по asset %d: %w", assetID, err)
	}
	if !avg.Valid {
		return nil, nil
	}
	v := avg.Float64
	return &v, nil
}

// clampScore зажимает сентимент-скор в [-1..1].
func clampScore(v float64) float64 {
	if v > 1 {
		return 1
	}
	if v < -1 {
		return -1
	}
	return v
}

// --- отображения строк БД в доменные типы ---

type assetRow struct {
	ID        int64     `db:"id"`
	CoinID    string    `db:"coin_id"`
	Symbol    string    `db:"symbol"`
	Name      string    `db:"name"`
	CreatedAt time.Time `db:"created_at"`
}

func (r assetRow) toModel() model.Asset {
	return model.Asset{ID: r.ID, CoinID: r.CoinID, Symbol: r.Symbol, Name: r.Name, CreatedAt: r.CreatedAt}
}

type sourceRow struct {
	ID        int64     `db:"id"`
	Slug      string    `db:"slug"`
	Name      string    `db:"name"`
	CreatedAt time.Time `db:"created_at"`
}

func (r sourceRow) toModel() model.Source {
	return model.Source{ID: r.ID, Slug: r.Slug, Name: r.Name, CreatedAt: r.CreatedAt}
}

type assetPriceRow struct {
	AssetID     int64           `db:"asset_id"`
	CoinID      string          `db:"coin_id"`
	Symbol      string          `db:"symbol"`
	Name        string          `db:"name"`
	PriceUSD    sql.NullFloat64 `db:"price_usd"`
	MarketCap   sql.NullFloat64 `db:"market_cap"`
	Volume      sql.NullFloat64 `db:"volume"`
	Change24H   sql.NullFloat64 `db:"change_24h"`
	LastUpdated sql.NullTime    `db:"last_updated"`
}

func (r assetPriceRow) toModel() model.AssetPrice {
	return model.AssetPrice{
		AssetID:     r.AssetID,
		CoinID:      r.CoinID,
		Symbol:      r.Symbol,
		Name:        r.Name,
		PriceUSD:    nullFloat(r.PriceUSD),
		MarketCap:   nullFloat(r.MarketCap),
		Volume:      nullFloat(r.Volume),
		Change24H:   nullFloat(r.Change24H),
		LastUpdated: nullTime(r.LastUpdated),
	}
}

func nullFloat(v sql.NullFloat64) float64 {
	if v.Valid {
		return v.Float64
	}
	return 0
}

func nullTime(v sql.NullTime) time.Time {
	if v.Valid {
		return v.Time
	}
	return time.Time{}
}

type indicatorSnapshotRow struct {
	ID           int64     `db:"id"`
	AssetID      int64     `db:"asset_id"`
	SourceID     int64     `db:"source_id"`
	RSI          float64   `db:"rsi"`
	ROC          float64   `db:"roc"`
	SMA7         float64   `db:"sma_7"`
	SMA20        float64   `db:"sma_20"`
	VolumeSignal float64   `db:"volume_signal"`
	CalculatedAt time.Time `db:"calculated_at"`
}

func (r indicatorSnapshotRow) toModel() model.IndicatorSnapshot {
	return model.IndicatorSnapshot{
		ID:           r.ID,
		AssetID:      r.AssetID,
		SourceID:     r.SourceID,
		RSI:          r.RSI,
		ROC:          r.ROC,
		SMA7:         r.SMA7,
		SMA20:        r.SMA20,
		VolumeSignal: r.VolumeSignal,
		CalculatedAt: r.CalculatedAt,
	}
}

type newsItemRow struct {
	ID               int64           `db:"id"`
	AssetID          sql.NullInt64   `db:"asset_id"`
	SourceID         int64           `db:"source_id"`
	ExternalID       string          `db:"external_id"`
	Title            string          `db:"title"`
	Body             string          `db:"body"`
	Link             string          `db:"link"`
	PublishedAt      time.Time       `db:"published_at"`
	SentimentScore   sql.NullFloat64 `db:"sentiment_score"`
	SentimentSummary sql.NullString  `db:"sentiment_summary"`
	InsertedAt       time.Time       `db:"inserted_at"`
}

func (r newsItemRow) toModel() model.NewsItem {
	var assetID *int64
	if r.AssetID.Valid {
		v := r.AssetID.Int64
		assetID = &v
	}
	var score *float64
	if r.SentimentScore.Valid {
		v := r.SentimentScore.Float64
		score = &v
	}
	var summary *string
	if r.SentimentSummary.Valid {
		v := r.SentimentSummary.String
		summary = &v
	}
	return model.NewsItem{
		ID:               r.ID,
		AssetID:          assetID,
		SourceID:         r.SourceID,
		ExternalID:       r.ExternalID,
		Title:            r.Title,
		Body:             r.Body,
		Link:             r.Link,
		PublishedAt:      r.PublishedAt,
		SentimentScore:   score,
		SentimentSummary: summary,
		InsertedAt:       r.InsertedAt,
	}
}

// Forecasts — репозиторий прогнозов и их факторной декомпозиции.
type Forecasts struct{ db *sqlx.DB }

func NewForecasts(db *sqlx.DB) *Forecasts { return &Forecasts{db: db} }

// SavePersisted — прогноз, готовый к записи: результат scoring + факторы.
// Используется репозиторием для одной транзакционной вставки.
type SavePersisted struct {
	model.Forecast
	Factors []model.ForecastFactor
}

// Save записывает новый прогноз и его факторы одной транзакцией, переводя
// предыдущий active-прогноз по активу в статус superseded. Возвращает id
// созданного прогноза.
func (r *Forecasts) Save(ctx context.Context, p SavePersisted) (int64, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx forecasts: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op при коммите

	// Предыдущий active → superseded (история остаётся для аудита).
	if _, err := tx.ExecContext(ctx,
		`UPDATE forecasts SET status = ? WHERE asset_id = ? AND status = ?`,
		string(model.ForecastStatusSuperseded), p.AssetID, string(model.ForecastStatusActive)); err != nil {
		return 0, fmt.Errorf("суперсед прогнозов asset %d: %w", p.AssetID, err)
	}

	res, err := tx.ExecContext(ctx, `
INSERT INTO forecasts (asset_id, created_at, horizon_hours, direction, confidence, risk_note, argument_text, raw_score, status)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.AssetID, p.CreatedAt.UTC(), p.HorizonHours, p.Direction, p.Confidence,
		p.RiskNote, p.ArgumentText, p.RawScore, string(model.ForecastStatusActive))
	if err != nil {
		return 0, fmt.Errorf("вставка forecast asset %d: %w", p.AssetID, err)
	}
	forecastID, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("last id forecast: %w", err)
	}

	for _, f := range p.Factors {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO forecast_factors (forecast_id, name, signal, base_weight, adjusted_weight, contribution, detail)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
			forecastID, f.Name, f.Signal, f.BaseWeight, f.AdjustedWeight, f.Contribution, f.Detail); err != nil {
			return 0, fmt.Errorf("вставка forecast_factor %s: %w", f.Name, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit forecast asset %d: %w", p.AssetID, err)
	}
	return forecastID, nil
}

// LatestByAsset возвращает последний active-прогноз по активу с его факторами.
// Если прогноза ещё нет — sql.ErrNoRows.
func (r *Forecasts) LatestByAsset(ctx context.Context, assetID int64) (model.Forecast, []model.ForecastFactor, error) {
	var fRow forecastRow
	const fq = `
SELECT id, asset_id, created_at, horizon_hours, direction, confidence, risk_note, argument_text, raw_score, status
FROM forecasts WHERE asset_id = ? AND status = ?
ORDER BY created_at DESC LIMIT 1`
	if err := r.db.GetContext(ctx, &fRow, fq, assetID, string(model.ForecastStatusActive)); err != nil {
		return model.Forecast{}, nil, fmt.Errorf("выборка forecast для asset %d: %w", assetID, err)
	}

	var facRows []forecastFactorRow
	const ffq = `
SELECT id, forecast_id, name, signal, base_weight, adjusted_weight, contribution, detail
FROM forecast_factors WHERE forecast_id = ? ORDER BY id`
	if err := r.db.SelectContext(ctx, &facRows, ffq, fRow.ID); err != nil {
		return model.Forecast{}, nil, fmt.Errorf("выборка forecast_factors для forecast %d: %w", fRow.ID, err)
	}
	factors := make([]model.ForecastFactor, 0, len(facRows))
	for _, fr := range facRows {
		factors = append(factors, fr.toModel())
	}
	return fRow.toModel(), factors, nil
}

// LatestAll возвращает последние active-прогнозы по всем активам (без факторов) —
// DTO для списка и главной страницы.
func (r *Forecasts) LatestAll(ctx context.Context) ([]model.ForecastSummary, error) {
	const q = `
SELECT
    f.asset_id   AS asset_id,
    a.symbol     AS symbol,
    a.name       AS name,
    f.direction  AS direction,
    f.confidence AS confidence,
    f.created_at AS created_at
FROM forecasts f
JOIN assets a ON a.id = f.asset_id
WHERE f.status = ?
AND f.id = (
    SELECT f2.id FROM forecasts f2
    WHERE f2.asset_id = f.asset_id AND f2.status = ?
    ORDER BY f2.created_at DESC LIMIT 1
)
ORDER BY a.id`
	var rows []forecastSummaryRow
	if err := r.db.SelectContext(ctx, &rows, q,
		string(model.ForecastStatusActive), string(model.ForecastStatusActive)); err != nil {
		return nil, fmt.Errorf("выборка последних прогнозов: %w", err)
	}
	out := make([]model.ForecastSummary, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.toModel())
	}
	return out, nil
}

// PendingResolution возвращает прогнозы старше olderThan, у которых ещё нет outcome
// (LEFT JOIN outcomes WHERE o.forecast_id IS NULL). Статус прогноза — active или
// superseded: superseded-прогнозы тоже должны сверяться по достижении горизонта.
// Это прогнозы, которые были выданы 24+ часов назад, но ещё не сверкнуты с фактом.
func (r *Forecasts) PendingResolution(ctx context.Context, olderThan time.Time) ([]model.Forecast, error) {
	const q = `
SELECT f.id, f.asset_id, f.created_at, f.horizon_hours, f.direction, f.confidence,
       f.risk_note, f.argument_text, f.raw_score, f.status
FROM forecasts f
LEFT JOIN outcomes o ON o.forecast_id = f.id
WHERE o.forecast_id IS NULL
  AND f.created_at < ?
  AND f.status IN (?, ?)
ORDER BY f.created_at ASC`
	var rows []forecastRow
	if err := r.db.SelectContext(ctx, &rows, q,
		olderThan.UTC(),
		string(model.ForecastStatusActive), string(model.ForecastStatusSuperseded)); err != nil {
		return nil, fmt.Errorf("выборка прогнозов для resolve: %w", err)
	}
	out := make([]model.Forecast, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.toModel())
	}
	return out, nil
}

// SetStatus переводит прогноз в указанный статус (для resolve — в resolved).
func (r *Forecasts) SetStatus(ctx context.Context, forecastID int64, status model.ForecastStatus) error {
	const q = `UPDATE forecasts SET status = ? WHERE id = ?`
	if _, err := r.db.ExecContext(ctx, q, string(status), forecastID); err != nil {
		return fmt.Errorf("обновление статуса forecast %d: %w", forecastID, err)
	}
	return nil
}

// FactorsByForecast возвращает факторы прогноза по его id — для атрибуции ошибок
// и обновления factor_stats в resolve-цикле.
func (r *Forecasts) FactorsByForecast(ctx context.Context, forecastID int64) ([]model.ForecastFactor, error) {
	var rows []forecastFactorRow
	const q = `
SELECT id, forecast_id, name, signal, base_weight, adjusted_weight, contribution, detail
FROM forecast_factors WHERE forecast_id = ? ORDER BY id`
	if err := r.db.SelectContext(ctx, &rows, q, forecastID); err != nil {
		return nil, fmt.Errorf("выборка forecast_factors для forecast %d: %w", forecastID, err)
	}
	out := make([]model.ForecastFactor, 0, len(rows))
	for _, fr := range rows {
		out = append(out, fr.toModel())
	}
	return out, nil
}

// PriceAtTime возвращает цену актива ближайшую к моменту ts (ts или ранее) —
// для price_at_forecast в resolve-логике. Если точек цен нет — sql.ErrNoRows.
func (r *Forecasts) PriceAtTime(ctx context.Context, assetID int64, ts time.Time) (float64, error) {
	const q = `
SELECT price_usd FROM price_points
WHERE asset_id = ? AND ts <= ?
ORDER BY ts DESC, source_id ASC LIMIT 1`
	var price float64
	if err := r.db.GetContext(ctx, &price, q, assetID, ts.UTC()); err != nil {
		return 0, fmt.Errorf("цена на момент %s для asset %d: %w", ts.UTC().Format(time.RFC3339), assetID, err)
	}
	return price, nil
}

// --- отображения строк БД прогнозов в доменные типы ---

type forecastRow struct {
	ID           int64     `db:"id"`
	AssetID      int64     `db:"asset_id"`
	CreatedAt    time.Time `db:"created_at"`
	HorizonHours int       `db:"horizon_hours"`
	Direction    string    `db:"direction"`
	Confidence   float64   `db:"confidence"`
	RiskNote     string    `db:"risk_note"`
	ArgumentText string    `db:"argument_text"`
	RawScore     float64   `db:"raw_score"`
	Status       string    `db:"status"`
}

func (r forecastRow) toModel() model.Forecast {
	return model.Forecast{
		ID:           r.ID,
		AssetID:      r.AssetID,
		CreatedAt:    r.CreatedAt,
		HorizonHours: r.HorizonHours,
		Direction:    r.Direction,
		Confidence:   r.Confidence,
		RiskNote:     r.RiskNote,
		ArgumentText: r.ArgumentText,
		RawScore:     r.RawScore,
		Status:       model.ForecastStatus(r.Status),
	}
}

type forecastFactorRow struct {
	ID             int64   `db:"id"`
	ForecastID     int64   `db:"forecast_id"`
	Name           string  `db:"name"`
	Signal         float64 `db:"signal"`
	BaseWeight     float64 `db:"base_weight"`
	AdjustedWeight float64 `db:"adjusted_weight"`
	Contribution   float64 `db:"contribution"`
	Detail         string  `db:"detail"`
}

func (r forecastFactorRow) toModel() model.ForecastFactor {
	return model.ForecastFactor{
		ID:             r.ID,
		ForecastID:     r.ForecastID,
		Name:           r.Name,
		Signal:         r.Signal,
		BaseWeight:     r.BaseWeight,
		AdjustedWeight: r.AdjustedWeight,
		Contribution:   r.Contribution,
		Detail:         r.Detail,
	}
}

type forecastSummaryRow struct {
	AssetID    int64     `db:"asset_id"`
	Symbol     string    `db:"symbol"`
	Name       string    `db:"name"`
	Direction  string    `db:"direction"`
	Confidence float64   `db:"confidence"`
	CreatedAt  time.Time `db:"created_at"`
}

func (r forecastSummaryRow) toModel() model.ForecastSummary {
	return model.ForecastSummary{
		AssetID:    r.AssetID,
		Symbol:     r.Symbol,
		Name:       r.Name,
		Direction:  r.Direction,
		Confidence: r.Confidence,
		CreatedAt:  r.CreatedAt,
	}
}

// --- T5: репозитории точности (outcomes, factor_stats) ---

// Outcomes — репозиторий результатов сверки прогнозов с фактом.
type Outcomes struct{ db *sqlx.DB }

func NewOutcomes(db *sqlx.DB) *Outcomes { return &Outcomes{db: db} }

// Insert записывает результат сверки прогноза. INSERT OR REPLACE — на случай
// повторного resolve (идемпотентность по forecast_id PK).
func (r *Outcomes) Insert(ctx context.Context, o model.Outcome) error {
	const q = `
INSERT OR REPLACE INTO outcomes
    (forecast_id, resolved_at, actual_direction, result, price_at_forecast,
     price_at_resolution, price_change_pct, culprit_factor, culprit_explanation)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := r.db.ExecContext(ctx, q,
		o.ForecastID, o.ResolvedAt.UTC(), o.ActualDirection, string(o.Result),
		o.PriceAtForecast, o.PriceAtResolution, o.PriceChangePct,
		o.CulpritFactor, o.CulpritExplanation)
	if err != nil {
		return fmt.Errorf("вставка outcome для forecast %d: %w", o.ForecastID, err)
	}
	return nil
}

// ExistsByForecast проверяет, есть ли уже результат сверки для прогноза.
func (r *Outcomes) ExistsByForecast(ctx context.Context, forecastID int64) (bool, error) {
	const q = `SELECT 1 FROM outcomes WHERE forecast_id = ? LIMIT 1`
	var one int
	if err := r.db.GetContext(ctx, &one, q, forecastID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("проверка outcome для forecast %d: %w", forecastID, err)
	}
	return true, nil
}

// ByForecast возвращает результат сверки для конкретного прогноза. Если прогноз
// ещё не сверкнут — sql.ErrNoRows.
func (r *Outcomes) ByForecast(ctx context.Context, forecastID int64) (model.Outcome, error) {
	var row outcomeRow
	const q = `
SELECT forecast_id, resolved_at, actual_direction, result, price_at_forecast,
       price_at_resolution, price_change_pct, culprit_factor, culprit_explanation
FROM outcomes WHERE forecast_id = ?`
	if err := r.db.GetContext(ctx, &row, q, forecastID); err != nil {
		return model.Outcome{}, fmt.Errorf("выборка outcome для forecast %d: %w", forecastID, err)
	}
	return row.toModel(), nil
}

// outcomeRow — строка таблицы outcomes.
type outcomeRow struct {
	ForecastID         int64     `db:"forecast_id"`
	ResolvedAt         time.Time `db:"resolved_at"`
	ActualDirection    string    `db:"actual_direction"`
	Result             string    `db:"result"`
	PriceAtForecast    float64   `db:"price_at_forecast"`
	PriceAtResolution  float64   `db:"price_at_resolution"`
	PriceChangePct     float64   `db:"price_change_pct"`
	CulpritFactor      string    `db:"culprit_factor"`
	CulpritExplanation string    `db:"culprit_explanation"`
}

func (r outcomeRow) toModel() model.Outcome {
	return model.Outcome{
		ForecastID:         r.ForecastID,
		ResolvedAt:         r.ResolvedAt,
		ActualDirection:    r.ActualDirection,
		Result:             model.OutcomeResult(r.Result),
		PriceAtForecast:    r.PriceAtForecast,
		PriceAtResolution:  r.PriceAtResolution,
		PriceChangePct:     r.PriceChangePct,
		CulpritFactor:      r.CulpritFactor,
		CulpritExplanation: r.CulpritExplanation,
	}
}

// RecentByAsset возвращает последние limit resolved-прогнозов по монете с их
// результатами сверки — DTO для секции «История точности» в UI.
func (r *Outcomes) RecentByAsset(ctx context.Context, assetID int64, limit int) ([]model.ForecastHistoryItem, error) {
	const q = `
SELECT
    f.id               AS forecast_id,
    f.created_at       AS created_at,
    f.direction        AS direction,
    f.confidence       AS confidence,
    o.result           AS result,
    o.culprit_factor   AS culprit_factor,
    o.culprit_explanation AS culprit_explanation,
    o.price_change_pct AS price_change_pct,
    o.actual_direction AS actual_direction
FROM outcomes o
JOIN forecasts f ON f.id = o.forecast_id
WHERE f.asset_id = ?
ORDER BY o.resolved_at DESC
LIMIT ?`
	var rows []forecastHistoryRow
	if err := r.db.SelectContext(ctx, &rows, q, assetID, limit); err != nil {
		return nil, fmt.Errorf("выборка истории outcomes для asset %d: %w", assetID, err)
	}
	out := make([]model.ForecastHistoryItem, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.toModel())
	}
	return out, nil
}

// RecentAll возвращает последние limit resolved-прогнозов по всем монетам —
// для расчёта глобальной сводки точности.
func (r *Outcomes) RecentAll(ctx context.Context, limit int) ([]model.ForecastHistoryItem, error) {
	const q = `
SELECT
    f.id               AS forecast_id,
    f.created_at       AS created_at,
    f.direction        AS direction,
    f.confidence       AS confidence,
    o.result           AS result,
    o.culprit_factor   AS culprit_factor,
    o.culprit_explanation AS culprit_explanation,
    o.price_change_pct AS price_change_pct,
    o.actual_direction AS actual_direction
FROM outcomes o
JOIN forecasts f ON f.id = o.forecast_id
ORDER BY o.resolved_at DESC
LIMIT ?`
	var rows []forecastHistoryRow
	if err := r.db.SelectContext(ctx, &rows, q, limit); err != nil {
		return nil, fmt.Errorf("выборка всех outcomes: %w", err)
	}
	out := make([]model.ForecastHistoryItem, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.toModel())
	}
	return out, nil
}

// ForecastHistoryRow — строка истории прогноза с результатом сверки.
type forecastHistoryRow struct {
	ForecastID         int64     `db:"forecast_id"`
	CreatedAt          time.Time `db:"created_at"`
	Direction          string    `db:"direction"`
	Confidence         float64   `db:"confidence"`
	Result             string    `db:"result"`
	CulpritFactor      string    `db:"culprit_factor"`
	CulpritExplanation string    `db:"culprit_explanation"`
	PriceChangePct     float64   `db:"price_change_pct"`
	ActualDirection    string    `db:"actual_direction"`
}

func (r forecastHistoryRow) toModel() model.ForecastHistoryItem {
	return model.ForecastHistoryItem{
		ForecastID:         r.ForecastID,
		CreatedAt:          r.CreatedAt,
		Direction:          r.Direction,
		Confidence:         r.Confidence,
		Result:             model.OutcomeResult(r.Result),
		CulpritFactor:      r.CulpritFactor,
		CulpritExplanation: r.CulpritExplanation,
		PriceChangePct:     r.PriceChangePct,
		ActualDirection:    r.ActualDirection,
	}
}

// FactorStats — репозиторий скользящей статистики точности факторов.
type FactorStats struct{ db *sqlx.DB }

func NewFactorStats(db *sqlx.DB) *FactorStats { return &FactorStats{db: db} }

// Get возвращает статистику фактора по монете. Если записи нет — возвращает
// значения по умолчанию (hit_rate_ema=0.5, samples=0) без ошибки.
func (r *FactorStats) Get(ctx context.Context, assetID int64, factor string) (model.FactorStat, error) {
	var row factorStatRow
	const q = `
SELECT asset_id, factor, hit_rate_ema, samples, updated_at
FROM factor_stats WHERE asset_id = ? AND factor = ?`
	if err := r.db.GetContext(ctx, &row, q, assetID, factor); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Нет данных — нейтральное значение по умолчанию.
			return model.FactorStat{AssetID: assetID, Factor: factor, HitRateEMA: 0.5, Samples: 0}, nil
		}
		return model.FactorStat{}, fmt.Errorf("выборка factor_stats (%d, %s): %w", assetID, factor, err)
	}
	return row.toModel(), nil
}

// ByAsset возвращает статистику всех факторов по монете. Факторы без записи
// в БД сюда не попадают (адаптация для них берёт дефолт через Get).
func (r *FactorStats) ByAsset(ctx context.Context, assetID int64) ([]model.FactorStat, error) {
	var rows []factorStatRow
	const q = `
SELECT asset_id, factor, hit_rate_ema, samples, updated_at
FROM factor_stats WHERE asset_id = ?`
	if err := r.db.SelectContext(ctx, &rows, q, assetID); err != nil {
		return nil, fmt.Errorf("выборка factor_stats для asset %d: %w", assetID, err)
	}
	out := make([]model.FactorStat, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.toModel())
	}
	return out, nil
}

// All возвращает всю статистику факторов (для глобальной сводки).
func (r *FactorStats) All(ctx context.Context) ([]model.FactorStat, error) {
	var rows []factorStatRow
	const q = `SELECT asset_id, factor, hit_rate_ema, samples, updated_at FROM factor_stats`
	if err := r.db.SelectContext(ctx, &rows, q); err != nil {
		return nil, fmt.Errorf("выборка всех factor_stats: %w", err)
	}
	out := make([]model.FactorStat, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.toModel())
	}
	return out, nil
}

// Upsert обновляет или вставляет статистику фактора по монете.
func (r *FactorStats) Upsert(ctx context.Context, stat model.FactorStat) error {
	const q = `
INSERT INTO factor_stats (asset_id, factor, hit_rate_ema, samples, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(asset_id, factor) DO UPDATE SET
    hit_rate_ema = excluded.hit_rate_ema,
    samples      = excluded.samples,
    updated_at   = excluded.updated_at`
	_, err := r.db.ExecContext(ctx, q,
		stat.AssetID, stat.Factor, stat.HitRateEMA, stat.Samples, stat.UpdatedAt.UTC())
	if err != nil {
		return fmt.Errorf("upsert factor_stats (%d, %s): %w", stat.AssetID, stat.Factor, err)
	}
	return nil
}

type factorStatRow struct {
	AssetID    int64     `db:"asset_id"`
	Factor     string    `db:"factor"`
	HitRateEMA float64   `db:"hit_rate_ema"`
	Samples    int       `db:"samples"`
	UpdatedAt  time.Time `db:"updated_at"`
}

func (r factorStatRow) toModel() model.FactorStat {
	return model.FactorStat{
		AssetID:    r.AssetID,
		Factor:     r.Factor,
		HitRateEMA: r.HitRateEMA,
		Samples:    r.Samples,
		UpdatedAt:  r.UpdatedAt,
	}
}
