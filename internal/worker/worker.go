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
//
// T5: отдельный resolve-цикл (раз в час) находит прогнозы старше 24ч без outcome,
// сверяет направление с фактической ценой, фиксирует hit/miss/neutral, находит
// «виновный» фактор (атрибуция) и обновляет factor_stats.hit_rate_ema. При расчёте
// новых прогнозов веса факторов адаптируются: adjusted_weight = base × clamp(ema/0.5,
// 0.5, 1.5) — фактор, что часто ошибается, получает понижение.
package worker

import (
	"context"
	"fmt"
	"log"
	"time"

	"test-future/internal/accuracy"
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
	// resolveInterval — период resolve-цикла (сверка прогнозов с фактом).
	resolveInterval = time.Hour
	// forecastHorizon — горизонт прогноза; прогнозы старше этого возраста подлежат resolve.
	forecastHorizon = 24 * time.Hour
	// accuracyHistoryLimit — сколько прогнозов в истории точности по монете для UI.
	accuracyHistoryLimit = 20
)

// Worker — фоновый опросник источников.
type Worker struct {
	cfg             config.Config
	taker           CoinGeckoTaker
	newsTaker       coinpaprika.NewsTaker // новости CoinPaprika
	rssTaker        rss.NewsTaker         // новости RSS (CoinDesk/Cointelegraph)
	assets          *storage.Assets
	sources         *storage.Sources
	priceStore      *storage.PricePoints
	logStore        *storage.UpdateLog
	indicatorStore  *storage.IndicatorSnapshots
	forecastStore   *storage.Forecasts
	newsStore       *storage.NewsItems
	sentiment       *sentiment.Service
	outcomeStore    *storage.Outcomes    // T5: результаты сверки прогнозов
	factorStatsRepo *storage.FactorStats // T5: статистика точности факторов
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
	outcomeStore *storage.Outcomes,
	factorStatsRepo *storage.FactorStats,
) *Worker {
	return NewWithTakers(
		cfg,
		coingecko.New(cfg.CoinGeckoBaseURL),
		coinpaprika.New(cfg.CoinPaprikaBaseURL),
		rss.New(),
		assets, sources, priceStore, logStore, indicatorStore, forecastStore,
		newsStore, sentimentSvc, outcomeStore, factorStatsRepo,
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
	outcomeStore *storage.Outcomes,
	factorStatsRepo *storage.FactorStats,
) *Worker {
	return &Worker{
		cfg:             cfg,
		taker:           taker,
		newsTaker:       newsTaker,
		rssTaker:        rssTaker,
		assets:          assets,
		sources:         sources,
		priceStore:      priceStore,
		logStore:        logStore,
		indicatorStore:  indicatorStore,
		forecastStore:   forecastStore,
		newsStore:       newsStore,
		sentiment:       sentimentSvc,
		outcomeStore:    outcomeStore,
		factorStatsRepo: factorStatsRepo,
	}
}

// Run блокирует до отмены ctx, опрашивая источник по расписанию.
// Первый опрос выполняется сразу (чтобы UI показал данные без ожидания).
// Отдельным тикером раз в forecastInterval пересчитываются прогнозы, а раз в
// resolveInterval сверяются прогнозы старше 24ч с фактом (resolve-цикл T5).
func (w *Worker) Run(ctx context.Context) {
	interval := time.Duration(w.cfg.FetchIntervalMin) * time.Minute
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	log.Printf("worker: запуск, интервал опроса %v, прогнозов — %v, resolve — %v",
		interval, forecastInterval, resolveInterval)

	// Сразу — один цикл цен/индикаторов/новостей/сентимента, затем прогноз и resolve.
	w.fetchOnce(ctx)
	w.computeForecasts(ctx)
	w.resolveOnce(ctx)

	priceTicker := time.NewTicker(interval)
	forecastTicker := time.NewTicker(forecastInterval)
	resolveTicker := time.NewTicker(resolveInterval)
	defer priceTicker.Stop()
	defer forecastTicker.Stop()
	defer resolveTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Printf("worker: остановлен")
			return
		case <-priceTicker.C:
			w.fetchOnce(ctx)
		case <-forecastTicker.C:
			w.computeForecasts(ctx)
		case <-resolveTicker.C:
			w.resolveOnce(ctx)
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

	// T5: адаптация весов — на основе factor_stats.hit_rate_ema считаем adjusted
	// веса (base × clamp(ema/0.5, 0.5, 1.5)) и передаём в scoring.Forecast для
	// перенормировки. Фактор без статистики использует EMA=0.5 (множитель 1.0).
	adjustedWeights := w.adaptedWeights(ctx, asset.ID, factors)
	result := scoring.Forecast(factors, adjustedWeights)

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

// adaptedWeights строит map фактор → адаптированный базовый вес на основе
// factor_stats.hit_rate_ema. Факторам без статистики — дефолтный base_weight.
// Возвращаемый map передаётся в scoring.Forecast как weights (нормировка идёт
// внутри scoring по сумме этих весов).
func (w *Worker) adaptedWeights(ctx context.Context, assetID int64, factors []scoring.Factor) map[scoring.FactorName]float64 {
	weights := make(map[scoring.FactorName]float64, len(factors))
	for _, f := range factors {
		base := scoring.DefaultBaseWeights[f.Name] // исходный вес из спеки T3/T4

		// T5: корректируем вес на основе hit_rate_ema этого фактора по монете.
		stat, err := w.factorStatsRepo.Get(ctx, assetID, string(f.Name))
		if err != nil {
			log.Printf("worker: factor_stats для %s asset %d: %v — использую base", f.Name, assetID, err)
			weights[f.Name] = base
			continue
		}
		weights[f.Name] = accuracy.AdjustedWeight(base, stat.HitRateEMA)
	}
	return weights
}

// resolveOnce находит прогнозы старше 24ч без outcome, сверяет их с фактом,
// фиксирует результат, атрибуцию и обновляет factor_stats. Ошибка на одном
// прогнозе не валит весь цикл.
func (w *Worker) resolveOnce(ctx context.Context) {
	cutoff := time.Now().UTC().Add(-forecastHorizon)
	pending, err := w.forecastStore.PendingResolution(ctx, cutoff)
	if err != nil {
		log.Printf("worker: resolve — выборка прогнозов: %v", err)
		return
	}
	if len(pending) == 0 {
		return
	}

	resolved := 0
	for _, fc := range pending {
		if err := w.resolveOne(ctx, fc); err != nil {
			log.Printf("worker: resolve прогноза %d: %v", fc.ID, err)
			continue
		}
		resolved++
	}
	log.Printf("worker: resolve — сверено прогнозов: %d (из %d)", resolved, len(pending))
}

// resolveOne сверяет один прогноз с фактом: берёт цены на момент прогноза и
// сейчас, считает результат, атрибуцию, пишет outcome и обновляет factor_stats.
func (w *Worker) resolveOne(ctx context.Context, fc model.Forecast) error {
	// Цена на момент прогноза — ближайшая точка ≤ created_at.
	priceAtForecast, err := w.forecastStore.PriceAtTime(ctx, fc.AssetID, fc.CreatedAt)
	if err != nil {
		return fmt.Errorf("цена на момент прогноза: %w", err)
	}

	// Текущая цена — последняя точка актива.
	priceAtResolution, err := w.lastPrice(ctx, fc.AssetID)
	if err != nil {
		return fmt.Errorf("текущая цена: %w", err)
	}

	result, changePct, actualDir := accuracy.Resolve(fc.Direction, priceAtForecast, priceAtResolution)

	// Факторы прогноза — для атрибуции и обновления статистики.
	factors, err := w.forecastStore.FactorsByForecast(ctx, fc.ID)
	if err != nil {
		return fmt.Errorf("факторы прогноза %d: %w", fc.ID, err)
	}

	// Атрибуция: при miss — виновный фактор, при hit — ведущий.
	culprit, explanation := "", ""
	switch result {
	case model.OutcomeMiss:
		culprit, explanation = accuracy.AttributeMiss(factors, actualDir)
	case model.OutcomeHit:
		culprit, explanation = accuracy.AttributeHit(factors, actualDir)
	}

	// Записываем outcome.
	outcome := model.Outcome{
		ForecastID:         fc.ID,
		ResolvedAt:         time.Now().UTC(),
		ActualDirection:    actualDir,
		Result:             result,
		PriceAtForecast:    priceAtForecast,
		PriceAtResolution:  priceAtResolution,
		PriceChangePct:     changePct,
		CulpritFactor:      culprit,
		CulpritExplanation: explanation,
	}
	if err := w.outcomeStore.Insert(ctx, outcome); err != nil {
		return fmt.Errorf("запись outcome: %w", err)
	}

	// Переводим прогноз в resolved (терминальный статус для аудит-истории).
	if err := w.forecastStore.SetStatus(ctx, fc.ID, model.ForecastStatusResolved); err != nil {
		return fmt.Errorf("обновление статуса прогноза: %w", err)
	}

	// Обновляем factor_stats по каждому фактору прогноза. Neutral не учитываем —
	// слишком маленькое движение (<0.5%) не несёт информации для обучения весов.
	w.updateFactorStats(ctx, fc.AssetID, factors, actualDir, result)

	return nil
}

// updateFactorStats обновляет hit_rate_ema для каждого фактора прогноза на основе
// совпадения знака его сигнала с actual_direction. При neutral обновление пропускается
// (движение цены слишком маленькое, чтобы служить сигналом для обучения).
func (w *Worker) updateFactorStats(ctx context.Context, assetID int64, factors []model.ForecastFactor, actualDir string, result model.OutcomeResult) {
	if result == model.OutcomeNeutral {
		return
	}
	for _, f := range factors {
		stat, err := w.factorStatsRepo.Get(ctx, assetID, f.Name)
		if err != nil {
			log.Printf("worker: factor_stats get (%s, %d): %v", f.Name, assetID, err)
			continue
		}
		newEMA, _ := accuracy.UpdateHitRateEMA(stat.HitRateEMA, f.Signal, actualDir)
		stat.HitRateEMA = newEMA
		stat.Samples++
		stat.UpdatedAt = time.Now().UTC()
		if err := w.factorStatsRepo.Upsert(ctx, stat); err != nil {
			log.Printf("worker: factor_stats upsert (%s, %d): %v", f.Name, assetID, err)
		}
	}
}

// lastPrice возвращает последнюю цену актива из price_points.
func (w *Worker) lastPrice(ctx context.Context, assetID int64) (float64, error) {
	price, err := w.priceStore.LatestByAssetID(ctx, assetID)
	if err != nil {
		return 0, err
	}
	return price.PriceUSD, nil
}
