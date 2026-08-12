// Package worker реализует фоновый цикл опроса источников.
//
// T2: кроме текущих цен (/coins/markets), worker тянет рыночный график за 30
// дней (/coins/{id}/market_chart) для каждой монеты, сохраняет ряды цен и
// объёмов в price_points, считает по ним технические индикаторы (RSI/ROC/SMA/
// VolumeSignal) и складывает последние значения в indicator_snapshots.
//
// T3: отдельный цикл прогноза (раз в час) читает индикаторы, считает прогноз
// «вверх/вниз за 24ч» через scoring.Forecast и сохраняет результат + декомпозицию
// по факторам в forecasts / forecast_factors.
//
// T4: в цикл опроса добавлена загрузка новостей (CoinPaprika + RSS) в news_items
// и батчевая оценка сентимента через OpenAI. В прогноз добавлен 4-й фактор
// sentiment (средний сентимент новостей по монете за 24ч). Без OPENAI_API_KEY
// прогноз работает на 3 факторах (graceful degradation).
package worker

import (
	"context"
	"fmt"
	"log"
	"time"

	"test-future/internal/config"
	"test-future/internal/indicator"
	"test-future/internal/model"
	"test-future/internal/scoring"
	"test-future/internal/sentiment"
	"test-future/internal/source/coingecko"
	"test-future/internal/source/coinpaprika"
	"test-future/internal/source/rss"
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
	// forecastInterval — период пересчёта прогнозов. По спеке T3 — раз в час.
	forecastInterval = time.Hour
	// newsLimit — сколько новостей тянуть за один запрос из CoinPaprika.
	newsLimit = 20
	// sentimentBatchLimit — сколько неотсентиченных новостей обрабатывать за цикл.
	sentimentBatchLimit = 20
	// sentimentWindow — период для среднего сентимента по монете.
	sentimentWindow = 24 * time.Hour
)

// Worker — фоновый опросник источников.
type Worker struct {
	cfg            config.Config
	taker          CoinGeckoTaker
	newsTaker      coinpaprika.NewsTaker // новости CoinPaprika
	rssTaker       rss.NewsTaker         // новости RSS (CoinDesk/Cointelegraph)
	assets         *storage.Assets
	sources        *storage.Sources
	priceStore     *storage.PricePoints
	logStore       *storage.UpdateLog
	indicatorStore *storage.IndicatorSnapshots
	forecastStore  *storage.Forecasts
	newsStore      *storage.NewsItems
	sentiment      *sentiment.Service
}

// New создаёт worker с реальными клиентами источников.
func New(
	cfg config.Config,
	assets *storage.Assets,
	sources *storage.Sources,
	priceStore *storage.PricePoints,
	logStore *storage.UpdateLog,
	indicatorStore *storage.IndicatorSnapshots,
	forecastStore *storage.Forecasts,
	newsStore *storage.NewsItems,
	sentimentSvc *sentiment.Service,
) *Worker {
	return NewWithTakers(
		cfg,
		coingecko.New(cfg.CoinGeckoBaseURL),
		coinpaprika.New(cfg.CoinPaprikaBaseURL),
		rss.New(),
		assets, sources, priceStore, logStore, indicatorStore, forecastStore,
		newsStore, sentimentSvc,
	)
}

// NewWithTakers позволяет внедрить тестовые/моковые реализации источников.
func NewWithTakers(
	cfg config.Config,
	taker CoinGeckoTaker,
	newsTaker coinpaprika.NewsTaker,
	rssTaker rss.NewsTaker,
	assets *storage.Assets,
	sources *storage.Sources,
	priceStore *storage.PricePoints,
	logStore *storage.UpdateLog,
	indicatorStore *storage.IndicatorSnapshots,
	forecastStore *storage.Forecasts,
	newsStore *storage.NewsItems,
	sentimentSvc *sentiment.Service,
) *Worker {
	return &Worker{
		cfg:            cfg,
		taker:          taker,
		newsTaker:      newsTaker,
		rssTaker:       rssTaker,
		assets:         assets,
		sources:        sources,
		priceStore:     priceStore,
		logStore:       logStore,
		indicatorStore: indicatorStore,
		forecastStore:  forecastStore,
		newsStore:      newsStore,
		sentiment:      sentimentSvc,
	}
}

// Run блокирует до отмены ctx, опрашивая источник по расписанию.
// Первый опрос выполняется сразу (чтобы UI показал данные без ожидания).
// Отдельным тикером раз в forecastInterval пересчитываются прогнозы.
func (w *Worker) Run(ctx context.Context) {
	interval := time.Duration(w.cfg.FetchIntervalMin) * time.Minute
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	log.Printf("worker: запуск, интервал опроса %v, прогнозов — %v", interval, forecastInterval)

	// Сразу — один цикл цен/индикаторов/новостей/сентимента, затем прогноз.
	w.fetchOnce(ctx)
	w.computeForecasts(ctx)

	priceTicker := time.NewTicker(interval)
	forecastTicker := time.NewTicker(forecastInterval)
	defer priceTicker.Stop()
	defer forecastTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Printf("worker: остановлен")
			return
		case <-priceTicker.C:
			w.fetchOnce(ctx)
		case <-forecastTicker.C:
			w.computeForecasts(ctx)
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

	// T4: новости из CoinPaprika + RSS и оценка сентимента.
	w.fetchNews(ctx)
	w.scoreNews(ctx)

	w.logCycle(ctx, started, model.UpdateStatusOK, added, nil)
	log.Printf("worker: цикл CoinGecko завершён, добавлено новых точек: %d", added)
}

// fetchNews тянет новости из всех источников (CoinPaprika + RSS) и пишет их в
// news_items с дедупом. Каждый источник логируется отдельно в update_log.
func (w *Worker) fetchNews(ctx context.Context) {
	byID, err := w.assets.MapByCoinID(ctx)
	if err != nil {
		log.Printf("worker: новости — выборка assets: %v", err)
		return
	}

	// CoinPaprika: один запрос на все монеты.
	if err := w.fetchCoinPaprikaNews(ctx, byID); err != nil {
		log.Printf("worker: новости CoinPaprika: %v", err)
	}

	// RSS: по одной ленте на источник.
	for _, slug := range []string{rss.SlugCoindesk, rss.SlugCointelegraph} {
		if err := w.fetchRSSNews(ctx, slug); err != nil {
			log.Printf("worker: новости %s: %v", slug, err)
		}
	}
}

// fetchCoinPaprikaNews тянет новости CoinPaprika, связывает с монетами и пишет в БД.
func (w *Worker) fetchCoinPaprikaNews(ctx context.Context, byID map[string]model.Asset) error {
	src, err := w.sources.BySlug(ctx, coinpaprika.SourceSlug)
	if err != nil {
		return fmt.Errorf("источник %s не найден: %w", coinpaprika.SourceSlug, err)
	}

	// Слаги CoinPaprika для отслеживаемых монет — единый источник правды в пакете.
	coinIDs := make([]string, 0, len(byID))
	for id := range byID {
		coinIDs = append(coinIDs, id)
	}
	slugs := coinpaprika.SlugsForCoinIDs(coinIDs)
	if len(slugs) == 0 {
		return nil
	}

	raw, err := w.newsTaker.News(ctx, slugs, newsLimit)
	if err != nil {
		return fmt.Errorf("запрос новостей: %w", err)
	}

	items := coinpaprika.NewsMap(raw, byID, src.ID)
	added, err := w.newsStore.InsertMany(ctx, items)
	if err != nil {
		return fmt.Errorf("вставка новостей: %w", err)
	}
	log.Printf("worker: новости CoinPaprika — добавлено новых: %d (из %d)", added, len(items))
	return nil
}

// fetchRSSNews тянет одну RSS-ленту и пишет новости в БД.
func (w *Worker) fetchRSSNews(ctx context.Context, slug string) error {
	src, err := w.sources.BySlug(ctx, slug)
	if err != nil {
		return fmt.Errorf("источник %s не найден: %w", slug, err)
	}

	raw, err := w.rssTaker.News(ctx, slug)
	if err != nil {
		return fmt.Errorf("парсинг ленты: %w", err)
	}

	items := rss.NewsMap(raw, src.ID)
	added, err := w.newsStore.InsertMany(ctx, items)
	if err != nil {
		return fmt.Errorf("вставка новостей: %w", err)
	}
	log.Printf("worker: новости %s — добавлено новых: %d (из %d)", slug, added, len(items))
	return nil
}

// scoreNews берёт батч неотсентиченных новостей и оценивает их сентимент через
// OpenAI. Если сервис выключен (нет API-ключа) — тихо пропускает (предупреждение
// уже выведено один раз при старте в main.go).
func (w *Worker) scoreNews(ctx context.Context) {
	if !w.sentiment.Enabled() {
		return
	}

	items, err := w.newsStore.Unscored(ctx, sentimentBatchLimit)
	if err != nil {
		log.Printf("worker: сентимент — выборка новостей: %v", err)
		return
	}
	if len(items) == 0 {
		return
	}

	scored, err := w.sentiment.ScoreBatch(ctx, items)
	if err != nil {
		log.Printf("worker: сентимент — ошибка батча: %v", err)
		return
	}
	log.Printf("worker: сентимент — оценено новостей: %d (из %d)", scored, len(items))
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

// computeForecasts пересчитывает прогнозы по всем активам: читает последние
// индикаторы, считает scoring.Forecast и сохраняет результат с декомпозицией
// по факторам. Ошибка на одной монете не валит весь цикл.
func (w *Worker) computeForecasts(ctx context.Context) {
	assets, err := w.assets.All(ctx)
	if err != nil {
		log.Printf("worker: прогнозы — выборка assets: %v", err)
		return
	}

	saved := 0
	for _, asset := range assets {
		if err := w.computeOneForecast(ctx, asset); err != nil {
			log.Printf("worker: прогноз для %s: %v", asset.CoinID, err)
			continue
		}
		saved++
	}
	log.Printf("worker: прогнозы пересчитаны, сохранено: %d", saved)
}

// computeOneForecast считает и сохраняет прогноз для одного актива.
func (w *Worker) computeOneForecast(ctx context.Context, asset model.Asset) error {
	snap, err := w.indicatorStore.ByAsset(ctx, asset.ID)
	if err != nil {
		// Индикаторы ещё не посчитаны — пропускаем (появятся после первого цикла).
		return fmt.Errorf("индикаторы для asset %d: %w", asset.ID, err)
	}

	in := scoring.IndicatorInput{
		RSI:          snap.RSI,
		ROC:          snap.ROC,
		SMA7:         snap.SMA7,
		SMA20:        snap.SMA20,
		VolumeSignal: snap.VolumeSignal,
	}

	// T4: средний сентимент новостей по монете за 24ч → 4-й фактор (если есть).
	since := time.Now().UTC().Add(-sentimentWindow)
	avgSentiment, err := w.newsStore.AvgSentimentByAsset(ctx, asset.ID, since)
	hasSentiment := err == nil && avgSentiment != nil
	sentimentScore := 0.0
	if hasSentiment {
		sentimentScore = *avgSentiment
	}
	factors := scoring.FactorsFromIndicatorsAndSentiment(in, sentimentScore, hasSentiment, indicator.DefaultVolumeTolerance)
	result := scoring.Forecast(factors, nil)

	// Собираем доменную модель для сохранения.
	now := time.Now().UTC()
	forecast := model.Forecast{
		AssetID:      asset.ID,
		CreatedAt:    now,
		HorizonHours: scoring.Horizon,
		Direction:    string(result.Direction),
		Confidence:   result.Confidence,
		RiskNote:     result.RiskNote,
		ArgumentText: result.ArgumentText,
		RawScore:     result.RawScore,
		Status:       model.ForecastStatusActive,
	}
	forecastFactors := make([]model.ForecastFactor, 0, len(result.Factors))
	for _, f := range result.Factors {
		forecastFactors = append(forecastFactors, model.ForecastFactor{
			Name:           string(f.Name),
			Signal:         f.Signal,
			BaseWeight:     f.BaseWeight,
			AdjustedWeight: f.AdjustedWeight,
			Contribution:   f.Contribution,
			Detail:         f.Detail,
		})
	}

	if _, err := w.forecastStore.Save(ctx, storage.SavePersisted{Forecast: forecast, Factors: forecastFactors}); err != nil {
		return fmt.Errorf("сохранение прогноза asset %d: %w", asset.ID, err)
	}
	return nil
}
