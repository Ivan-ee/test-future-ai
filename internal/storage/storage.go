// Package storage — репозитории поверх sqlx для доменных сущностей.
//
// Репозитории инкапсулируют SQL и трансляцию строк в доменные типы из model.
// Слой source пишет сюда сырые наблюдения; слой server читает отсюда
// агрегированные DTO для API.
package storage

import (
	"context"
	"database/sql"
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
