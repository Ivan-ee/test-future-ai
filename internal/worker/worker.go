// Package worker реализует фоновый цикл опроса источников.
//
// T2: кроме текущих цен (/coins/markets), worker тянет рыночный график за 30
// дней (/coins/{id}/market_chart) для каждой монеты, сохраняет ряды цен и
// объёмов в price_points, считает по ним технические индикаторы (RSI/ROC/SMA/
// VolumeSignal) и складывает последние значения в indicator_snapshots.
package worker

import (
	"context"
	"fmt"
	"log"
	"time"

	"test-future/internal/config"
	"test-future/internal/indicator"
	"test-future/internal/model"
	"test-future/internal/source/coingecko"
	"test-future/internal/storage"
)

// CoinGeckoTaker — контракт на сетевой источник цен и рядов для worker.
// Совпадает с coingecko.ChartTaker; отдельный тип держит зависимость worker
// от конкретного источника явной.
type CoinGeckoTaker = coingecko.ChartTaker

// Параметры расчёта индикаторов (из спеки T2).
const (
	chartDays      = 30 // глубина рыночного графика, дней
	rsiPeriod      = 14
	rocPeriod      = 10
	smaShortPeriod = 7
	smaLongPeriod  = 20
	volumePeriod   = 14
	// Зона нормы VolumeSignal берётся из indicator.DefaultVolumeTolerance —
	// единый источник правды для расчёта и интерпретации.
	// Сколько точек ряда грузить из БД для расчёта — с запасом под самый длинный
	// индикатор (SMA20 + RSI14 нужен 21+).
	closesWindow = 30
)

// Worker — фоновый опросник источников.
type Worker struct {
	cfg            config.Config
	taker          CoinGeckoTaker
	assets         *storage.Assets
	sources        *storage.Sources
	priceStore     *storage.PricePoints
	logStore       *storage.UpdateLog
	indicatorStore *storage.IndicatorSnapshots
}

// New создаёт worker с реальным клиентом CoinGecko.
func New(
	cfg config.Config,
	assets *storage.Assets,
	sources *storage.Sources,
	priceStore *storage.PricePoints,
	logStore *storage.UpdateLog,
	indicatorStore *storage.IndicatorSnapshots,
) *Worker {
	return NewWithTaker(cfg, coingecko.New(cfg.CoinGeckoBaseURL), assets, sources, priceStore, logStore, indicatorStore)
}

// NewWithTaker позволяет внедрить тестовую/моковую реализацию источника.
func NewWithTaker(
	cfg config.Config,
	taker CoinGeckoTaker,
	assets *storage.Assets,
	sources *storage.Sources,
	priceStore *storage.PricePoints,
	logStore *storage.UpdateLog,
	indicatorStore *storage.IndicatorSnapshots,
) *Worker {
	return &Worker{
		cfg:            cfg,
		taker:          taker,
		assets:         assets,
		sources:        sources,
		priceStore:     priceStore,
		logStore:       logStore,
		indicatorStore: indicatorStore,
	}
}

// Run блокирует до отмены ctx, опрашивая источник по расписанию.
// Первый опрос выполняется сразу (чтобы UI показал данные без ожидания).
func (w *Worker) Run(ctx context.Context) {
	interval := time.Duration(w.cfg.FetchIntervalMin) * time.Minute
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	log.Printf("worker: запуск, интервал опроса %v", interval)

	// Сразу — один цикл, дальше по тикеру.
	w.fetchOnce(ctx)

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Printf("worker: остановлен")
			return
		case <-t.C:
			w.fetchOnce(ctx)
		}
	}
}

// fetchOnce выполняет один цикл опроса CoinGecko: тянет цены, пишет в БД,
// фиксирует результат в журнале. Ошибки не прерывают цикл.
func (w *Worker) fetchOnce(ctx context.Context) {
	started := time.Now().UTC()

	byID, err := w.assets.MapByCoinID(ctx)
	if err != nil {
		w.logCycle(ctx, started, model.UpdateStatusError, 0, fmt.Errorf("выборка assets: %w", err))
		return
	}
	if len(byID) == 0 {
		w.logCycle(ctx, started, model.UpdateStatusError, 0, fmt.Errorf("нет отслеживаемых assets"))
		return
	}

	src, err := w.sources.BySlug(ctx, coingecko.SourceSlug)
	if err != nil {
		w.logCycle(ctx, started, model.UpdateStatusError, 0, fmt.Errorf("источник %s не найден: %w", coingecko.SourceSlug, err))
		return
	}

	coinIDs := make([]string, 0, len(byID))
	for id := range byID {
		coinIDs = append(coinIDs, id)
	}

	coins, err := w.taker.Markets(ctx, coinIDs)
	if err != nil {
		w.logCycle(ctx, started, model.UpdateStatusError, 0, err)
		return
	}

	points := coingecko.MarketMap(coins, byID, src.ID)
	added := 0
	for _, p := range points {
		ok, err := w.priceStore.InsertOne(ctx, p)
		if err != nil {
			// Не валим весь цикл из-за одной записи: логируем и идём дальше.
			log.Printf("worker: вставка price_point для asset %d: %v", p.AssetID, err)
			continue
		}
		if ok {
			added++
		}
	}

	// T2: рыночные графики за 30 дней + расчёт индикаторов по каждой монете.
	for coinID, asset := range byID {
		if err := w.fetchChartAndIndicators(ctx, asset, coinID, src.ID); err != nil {
			// Ошибка на одной монете не валит весь цикл.
			log.Printf("worker: индикаторы для %s: %v", coinID, err)
		}
	}

	w.logCycle(ctx, started, model.UpdateStatusOK, added, nil)
	log.Printf("worker: цикл CoinGecko завершён, добавлено новых точек: %d", added)
}

// fetchChartAndIndicators тянет рыночный график монеты за chartDays дней,
// сохраняет ряды цен/объёмов в price_points, затем считает индикаторы по данным
// из БД (единый источник правды) и сохраняет снапшот в indicator_snapshots.
func (w *Worker) fetchChartAndIndicators(ctx context.Context, asset model.Asset, coinID string, sourceID int64) error {
	chart, err := w.taker.MarketChart(ctx, coinID, chartDays)
	if err != nil {
		return fmt.Errorf("market_chart %s: %w", coinID, err)
	}

	// Сохраняем исторический ряд в price_points (дедуп обеспечит InsertOne).
	chartPoints := coingecko.ChartMap(chart, asset.ID, sourceID)
	for _, p := range chartPoints {
		if _, err := w.priceStore.InsertOne(ctx, p); err != nil {
			log.Printf("worker: вставка chart-point для asset %d: %v", asset.ID, err)
		}
	}

	// Грузим ряды из БД — единый источник правды для индикаторов.
	closes, err := w.priceStore.LastClosesByAsset(ctx, asset.ID, closesWindow)
	if err != nil {
		return fmt.Errorf("выборка closes asset %d: %w", asset.ID, err)
	}
	volumes, err := w.priceStore.LastVolumesByAsset(ctx, asset.ID, closesWindow)
	if err != nil {
		return fmt.Errorf("выборка volumes asset %d: %w", asset.ID, err)
	}

	// RSI и ROC требуют минимум period+1 точек. Если данных мало — пропускаем
	// (на первых циклах ряда может не хватать).
	if len(closes) < rocPeriod+1 {
		return nil
	}

	rsi, err := indicator.RSI(closes, rsiPeriod)
	if err != nil {
		return fmt.Errorf("RSI asset %d: %w", asset.ID, err)
	}
	roc, err := indicator.ROC(closes, rocPeriod)
	if err != nil {
		return fmt.Errorf("ROC asset %d: %w", asset.ID, err)
	}
	sma7, err := indicator.SMA(closes, smaShortPeriod)
	if err != nil {
		return fmt.Errorf("SMA7 asset %d: %w", asset.ID, err)
	}
	sma20, err := indicator.SMA(closes, smaLongPeriod)
	if err != nil {
		return fmt.Errorf("SMA20 asset %d: %w", asset.ID, err)
	}

	// VolumeSignal требует минимум volumePeriod точек; если данных мало — 0.
	volSignal := 0.0
	if len(volumes) >= volumePeriod {
		volSignal, err = indicator.VolumeSignal(volumes, volumePeriod, indicator.DefaultVolumeTolerance)
		if err != nil {
			log.Printf("worker: VolumeSignal asset %d: %v", asset.ID, err)
		}
	}

	snap := model.IndicatorSnapshot{
		AssetID:      asset.ID,
		SourceID:     sourceID,
		RSI:          rsi,
		ROC:          roc,
		SMA7:         sma7,
		SMA20:        sma20,
		VolumeSignal: volSignal,
		CalculatedAt: time.Now().UTC(),
	}
	if err := w.indicatorStore.Upsert(ctx, snap); err != nil {
		return fmt.Errorf("upsert indicators asset %d: %w", asset.ID, err)
	}
	return nil
}

// logCycle фиксирует результат цикла в update_log, приводя ошибку к тексту.
func (w *Worker) logCycle(ctx context.Context, started time.Time, status model.UpdateStatus, added int, err error) {
	entry := model.UpdateLog{
		SourceSlug: coingecko.SourceSlug,
		Status:     status,
		ItemsAdded: added,
		StartedAt:  started,
		FinishedAt: time.Now().UTC(),
	}
	if err != nil {
		entry.Error = err.Error()
	}
	if logErr := w.logStore.Record(ctx, entry); logErr != nil {
		log.Printf("worker: не удалось записать update_log: %v", logErr)
	}
}
