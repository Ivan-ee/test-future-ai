// Package worker реализует фоновый цикл опроса источников.
//
// В T1 один источник (CoinGecko) и одна задача — fetch цен по крону.
// Worker запускается горутиной из main, опрашивает источник каждые
// cfg.FetchIntervalMin минут, пишет точки цен (с дедупом) и фиксирует
// результат в update_log.
package worker

import (
	"context"
	"fmt"
	"log"
	"time"

	"test-future/internal/config"
	"test-future/internal/model"
	"test-future/internal/source/coingecko"
	"test-future/internal/storage"
)

// CoinGeckoTaker — контракт на сетевой источник цен для worker.
// Совпадает с coingecko.MarketsTaker; отдельный тип держит зависимость
// worker от конкретного источника явной.
type CoinGeckoTaker = coingecko.MarketsTaker

// Worker — фоновый опросник источников.
type Worker struct {
	cfg        config.Config
	taker      CoinGeckoTaker
	assets     *storage.Assets
	sources    *storage.Sources
	priceStore *storage.PricePoints
	logStore   *storage.UpdateLog
}

// New создаёт worker с реальным клиентом CoinGecko.
func New(
	cfg config.Config,
	assets *storage.Assets,
	sources *storage.Sources,
	priceStore *storage.PricePoints,
	logStore *storage.UpdateLog,
) *Worker {
	return NewWithTaker(cfg, coingecko.New(cfg.CoinGeckoBaseURL), assets, sources, priceStore, logStore)
}

// NewWithTaker позволяет внедрить тестовую/моковую реализацию источника.
func NewWithTaker(
	cfg config.Config,
	taker CoinGeckoTaker,
	assets *storage.Assets,
	sources *storage.Sources,
	priceStore *storage.PricePoints,
	logStore *storage.UpdateLog,
) *Worker {
	return &Worker{
		cfg:        cfg,
		taker:      taker,
		assets:     assets,
		sources:    sources,
		priceStore: priceStore,
		logStore:   logStore,
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

	w.logCycle(ctx, started, model.UpdateStatusOK, added, nil)
	log.Printf("worker: цикл CoinGecko завершён, добавлено новых точек: %d", added)
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
