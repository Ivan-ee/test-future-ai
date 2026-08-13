package worker

import (
	"context"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"test-future/internal/config"
	"test-future/internal/db"
	"test-future/internal/model"
	"test-future/internal/scoring"
	"test-future/internal/sentiment"
	"test-future/internal/source/coingecko"
	"test-future/internal/source/coinpaprika"
	"test-future/internal/source/rss"
	"test-future/internal/storage"
)

// fakeTaker — тестовый источник CoinGecko: отдаёт заранее заданные монеты и
// рыночные графики. Реализует coingecko.ChartTaker.
type fakeTaker struct {
	coins  []coingecko.MarketCoin
	err    error
	charts map[string]coingecko.MarketChart
}

func (f fakeTaker) Markets(_ context.Context, _ []string) ([]coingecko.MarketCoin, error) {
	return f.coins, f.err
}

func (f fakeTaker) MarketChart(_ context.Context, coinID string, _ int) (coingecko.MarketChart, error) {
	if f.err != nil {
		return coingecko.MarketChart{}, f.err
	}
	if c, ok := f.charts[coinID]; ok {
		return c, nil
	}
	return coingecko.MarketChart{}, nil
}

// fakeNewsTaker — тестовый источник новостей CoinPaprika.
type fakeNewsTaker struct {
	news []coinpaprika.RawNews
	err  error
}

func (f fakeNewsTaker) News(_ context.Context, _ []string, _ int) ([]coinpaprika.RawNews, error) {
	return f.news, f.err
}

// fakeRSSTaker — тестовый источник RSS-новостей.
type fakeRSSTaker struct {
	news map[string][]rss.RawNews // slug → новости
	err  error
}

func (f fakeRSSTaker) News(_ context.Context, slug string) ([]rss.RawNews, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.news[slug], nil
}

// newWorkerDB открывает тестовую БД с сидингом.
func newWorkerDB(t *testing.T) *sqlx.DB {
	t.Helper()
	d, err := db.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("открытие тестовой БД: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// workerDeps — набор репозиториев + сентимент-сервис для тестового worker.
// Собирается один раз и переиспользуется, чтобы не дублировать параметры.
type workerDeps struct {
	assets      *storage.Assets
	sources     *storage.Sources
	prices      *storage.PricePoints
	logs        *storage.UpdateLog
	indicators  *storage.IndicatorSnapshots
	forecasts   *storage.Forecasts
	news        *storage.NewsItems
	sentiment   *sentiment.Service
	outcomes    *storage.Outcomes    // T5
	factorStats *storage.FactorStats // T5
}

func newWorkerDeps(d *sqlx.DB) workerDeps {
	news := storage.NewNewsItems(d)
	return workerDeps{
		assets:      storage.NewAssets(d),
		sources:     storage.NewSources(d),
		prices:      storage.NewPricePoints(d),
		logs:        storage.NewUpdateLog(d),
		indicators:  storage.NewIndicatorSnapshots(d),
		forecasts:   storage.NewForecasts(d),
		news:        news,
		sentiment:   sentiment.New("", "", "", news), // noop по умолчанию
		outcomes:    storage.NewOutcomes(d),
		factorStats: storage.NewFactorStats(d),
	}
}

// newTestWorker собирает worker с заданными фейковыми источниками.
func newTestWorker(t *testing.T, d *sqlx.DB, taker fakeTaker, deps workerDeps) *Worker {
	t.Helper()
	return NewWithTakers(
		config.Config{FetchIntervalMin: 10},
		taker,
		fakeNewsTaker{},
		fakeRSSTaker{},
		deps.assets, deps.sources, deps.prices, deps.logs,
		deps.indicators, deps.forecasts, deps.news, deps.sentiment,
		deps.outcomes, deps.factorStats,
	)
}

// TestFetchOnce_WritesPointsAndLog — один цикл worker: тянет цены, пишет точки
// и успешную запись в update_log. Проверяем сквозной путь источник→БД.
func TestFetchOnce_WritesPointsAndLog(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newWorkerDB(t)
	deps := newWorkerDeps(d)

	taker := fakeTaker{coins: []coingecko.MarketCoin{
		{ID: "bitcoin", Symbol: "btc", Name: "Bitcoin",
			CurrentPrice: 60000, MarketCap: 1e12, TotalVolume: 3e10,
			PriceChangePercentage24H: 1.5, LastUpdated: "2026-08-12T10:00:00Z"},
		{ID: "ethereum", Symbol: "eth", Name: "Ethereum",
			CurrentPrice: 3000, MarketCap: 3.6e11, TotalVolume: 1.5e10,
			PriceChangePercentage24H: -0.5, LastUpdated: "2026-08-12T10:00:00Z"},
	}}
	w := newTestWorker(t, d, taker, deps)

	w.fetchOnce(ctx)

	// Должно появиться 2 точки цен.
	var nPoints int
	if err := d.GetContext(ctx, &nPoints, `SELECT COUNT(*) FROM price_points`); err != nil {
		t.Fatalf("count price_points: %v", err)
	}
	if nPoints != 2 {
		t.Errorf("хотели 2 price_points, получили %d", nPoints)
	}

	// Должна появиться одна успешная запись в update_log со статусом ok.
	var nLog int
	if err := d.GetContext(ctx, &nLog, `SELECT COUNT(*) FROM update_log`); err != nil {
		t.Fatalf("count update_log: %v", err)
	}
	if nLog != 1 {
		t.Fatalf("хотели 1 запись update_log, получили %d", nLog)
	}
	var status string
	var itemsAdded int
	if err := d.QueryRowxContext(ctx,
		`SELECT status, items_added FROM update_log WHERE source_slug='coingecko'`).
		Scan(&status, &itemsAdded); err != nil {
		t.Fatalf("select update_log: %v", err)
	}
	if model.UpdateStatus(status) != model.UpdateStatusOK {
		t.Errorf("status: хотели ok, получили %s", status)
	}
	if itemsAdded != 2 {
		t.Errorf("items_added: хотели 2, получили %d", itemsAdded)
	}
}

// TestFetchOnce_SourceErrorLogged — при ошибке источника цикл не падает, а
// фиксирует статус error в update_log и не пишет точки.
func TestFetchOnce_SourceErrorLogged(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newWorkerDB(t)
	deps := newWorkerDeps(d)

	w := newTestWorker(t, d, fakeTaker{err: errFake("источник недоступен")}, deps)

	w.fetchOnce(ctx)

	var nPoints int
	if err := d.GetContext(ctx, &nPoints, `SELECT COUNT(*) FROM price_points`); err != nil {
		t.Fatalf("count price_points: %v", err)
	}
	if nPoints != 0 {
		t.Errorf("при ошибке не должно быть price_points, получили %d", nPoints)
	}

	var status, errText string
	if err := d.QueryRowxContext(ctx,
		`SELECT status, error FROM update_log WHERE source_slug='coingecko'`).
		Scan(&status, &errText); err != nil {
		t.Fatalf("select update_log: %v", err)
	}
	if model.UpdateStatus(status) != model.UpdateStatusError {
		t.Errorf("status: хотели error, получили %s", status)
	}
	if errText == "" {
		t.Errorf("текст ошибки должен быть заполнен")
	}
}

// TestFetchOnce_DedupOnRetry — повторный цикл с теми же данными не добавляет
// дублей (дедуп по ключу), items_added во втором цикле = 0.
func TestFetchOnce_DedupOnRetry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newWorkerDB(t)
	deps := newWorkerDeps(d)

	w := newTestWorker(t, d, fakeTaker{coins: []coingecko.MarketCoin{
		{ID: "bitcoin", CurrentPrice: 60000, LastUpdated: "2026-08-12T10:00:00Z"},
	}}, deps)

	w.fetchOnce(ctx)
	w.fetchOnce(ctx) // те же данные — дедуп

	var nPoints int
	if err := d.GetContext(ctx, &nPoints, `SELECT COUNT(*) FROM price_points`); err != nil {
		t.Fatalf("count price_points: %v", err)
	}
	if nPoints != 1 {
		t.Errorf("после двух циклов должна быть 1 точка (дедуп), получили %d", nPoints)
	}
}

// errFake — простой тип ошибки для фейкового источника.
type errFake string

func (e errFake) Error() string { return string(e) }

// buildMonotonicChart — рыночный график с монотонно растущим рядом из n точек
// (цены 1..n, объёмы постоянные 1000). ts — ежедневные начиная с base.
func buildMonotonicChart(n int, baseMs int64) coingecko.MarketChart {
	prices := make([][2]float64, n)
	volumes := make([][2]float64, n)
	for i := range n {
		ts := baseMs + int64(i)*86400000 // +1 день в мс
		prices[i] = [2]float64{float64(ts), float64(i + 1)}
		volumes[i] = [2]float64{float64(ts), 1000}
	}
	return coingecko.MarketChart{Prices: prices, TotalVolumes: volumes}
}

// TestFetchOnce_IndicatorsComputed — один цикл worker с монотонным рядом в chart:
// в indicator_snapshots появляется снапшот с RSI≈100 (только рост).
func TestFetchOnce_IndicatorsComputed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newWorkerDB(t)
	deps := newWorkerDeps(d)

	// Монотонный ряд из 25 точек → достаточно для RSI(14)/ROC(10)/SMA(20).
	chart := buildMonotonicChart(25, 1723334400000)
	// LastUpdated точки Markets совпадает с последней точкой chart, чтобы ряд
	// был непрерывным; TotalVolume=1000 как в chart — корректный объём.
	lastChartTS := "2024-09-04T00:00:00Z"
	taker := fakeTaker{
		coins: []coingecko.MarketCoin{
			{ID: "bitcoin", Symbol: "btc", Name: "Bitcoin",
				CurrentPrice: 25, TotalVolume: 1000, LastUpdated: lastChartTS},
		},
		charts: map[string]coingecko.MarketChart{"bitcoin": chart},
	}
	w := newTestWorker(t, d, taker, deps)

	w.fetchOnce(ctx)

	snap, err := deps.indicators.ByAsset(ctx, 1) // bitcoin → asset_id=1 из сидинга
	if err != nil {
		t.Fatalf("снапшот индикаторов должен быть сохранён: %v", err)
	}
	// Монотонный рост → RSI≈100, ROC>0, SMA(7)>SMA(20).
	if snap.RSI < 90 {
		t.Errorf("RSI монотонного ряда: хотели ≈100, получили %v", snap.RSI)
	}
	if snap.ROC <= 0 {
		t.Errorf("ROC монотонного ряда: хотели >0, получили %v", snap.ROC)
	}
	if snap.SMA7 <= snap.SMA20 {
		t.Errorf("хотели SMA7>SMA20: получили %v и %v", snap.SMA7, snap.SMA20)
	}
	// VolumeSignal при постоянных объёмах → ≈1.
	if snap.VolumeSignal < 0.99 || snap.VolumeSignal > 1.01 {
		t.Errorf("VolumeSignal постоянного ряда: хотели ≈1, получили %v", snap.VolumeSignal)
	}
}

// TestFetchOnce_IndicatorsSkippedOnShortSeries — если ряда слишком мало для
// расчёта (меньше rocPeriod+1 точек), снапшот не создаётся, но цикл не падает.
func TestFetchOnce_IndicatorsSkippedOnShortSeries(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newWorkerDB(t)
	deps := newWorkerDeps(d)

	// Ряд из 5 точек — меньше rocPeriod(10)+1=11, расчёт пропускается.
	chart := buildMonotonicChart(5, 1723334400000)
	taker := fakeTaker{
		coins: []coingecko.MarketCoin{
			{ID: "bitcoin", CurrentPrice: 5, LastUpdated: "2026-08-12T10:00:00Z"},
		},
		charts: map[string]coingecko.MarketChart{"bitcoin": chart},
	}
	w := newTestWorker(t, d, taker, deps)

	w.fetchOnce(ctx)

	// Снапшота нет — цикл корректно пропустил расчёт.
	if _, err := deps.indicators.ByAsset(ctx, 1); err == nil {
		t.Fatal("не ожидали снапшот при коротком ряде")
	}

	// Журнал при этом успешен.
	var status string
	if err := d.QueryRowxContext(ctx,
		`SELECT status FROM update_log WHERE source_slug='coingecko'`).Scan(&status); err != nil {
		t.Fatalf("select update_log: %v", err)
	}
	if model.UpdateStatus(status) != model.UpdateStatusOK {
		t.Errorf("status: хотели ok, получили %s", status)
	}
}

// TestComputeForecasts_FromIndicators — сквозной путь: после цикла цен/индикаторов
// цикл прогнозов читает индикаторы из БД, считает scoring.Forecast и сохраняет
// результат + декомпозицию по факторам. Монотонно растущий ряд → прогноз «вверх».
func TestComputeForecasts_FromIndicators(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newWorkerDB(t)
	deps := newWorkerDeps(d)

	chart := buildMonotonicChart(25, 1723334400000)
	taker := fakeTaker{
		coins: []coingecko.MarketCoin{
			{ID: "bitcoin", Symbol: "btc", Name: "Bitcoin",
				CurrentPrice: 25, TotalVolume: 1000, LastUpdated: "2024-09-04T00:00:00Z"},
		},
		charts: map[string]coingecko.MarketChart{"bitcoin": chart},
	}
	w := newTestWorker(t, d, taker, deps)

	// Сначала цикл цен/индикаторов, затем — прогнозов.
	w.fetchOnce(ctx)
	w.computeForecasts(ctx)

	// Должен появиться активный прогноз по bitcoin (asset_id=1).
	fc, factors, err := deps.forecasts.LatestByAsset(ctx, 1)
	if err != nil {
		t.Fatalf("прогноз должен быть сохранён: %v", err)
	}
	if fc.Direction != "up" {
		t.Errorf("монотонный рост: хотели direction=up, получили %s", fc.Direction)
	}
	if fc.Confidence < 0.5 || fc.Confidence > 1.0 {
		t.Errorf("confidence вне [0.5,1.0]: %v", fc.Confidence)
	}
	if fc.HorizonHours != 24 {
		t.Errorf("horizon: хотели 24, получили %d", fc.HorizonHours)
	}
	if fc.ArgumentText == "" {
		t.Error("argument_text не должен быть пустым")
	}
	// Без новостей → прогноз на 3 факторах (graceful degradation).
	if len(factors) != 3 {
		t.Errorf("хотели 3 фактора (без sentiment), получили %d", len(factors))
	}

	// Повторный расчёт — предыдущий прогноз уходит в superseded, новый active.
	w.computeForecasts(ctx)
	var nActive, nSuperseded int
	if err := d.GetContext(ctx, &nActive, `SELECT COUNT(*) FROM forecasts WHERE status='active'`); err != nil {
		t.Fatalf("count active: %v", err)
	}
	if err := d.GetContext(ctx, &nSuperseded, `SELECT COUNT(*) FROM forecasts WHERE status='superseded'`); err != nil {
		t.Fatalf("count superseded: %v", err)
	}
	if nActive != 1 {
		t.Errorf("хотели 1 active прогноз, получили %d", nActive)
	}
	if nSuperseded != 1 {
		t.Errorf("хотели 1 superseded прогноз (история), получили %d", nSuperseded)
	}
}

// TestComputeForecasts_SkipsWithoutIndicators — если индикаторы ещё не посчитаны,
// цикл прогнозов не падает и не создаёт прогнозов.
func TestComputeForecasts_SkipsWithoutIndicators(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newWorkerDB(t)
	deps := newWorkerDeps(d)

	w := newTestWorker(t, d, fakeTaker{coins: []coingecko.MarketCoin{
		{ID: "bitcoin", CurrentPrice: 60000, LastUpdated: "2026-08-12T10:00:00Z"},
	}}, deps)

	// fetchOnce не дал исторического ряда → индикаторов нет.
	w.fetchOnce(ctx)
	w.computeForecasts(ctx)

	var n int
	if err := d.GetContext(ctx, &n, `SELECT COUNT(*) FROM forecasts`); err != nil {
		t.Fatalf("count forecasts: %v", err)
	}
	if n != 0 {
		t.Errorf("без индикаторов не должно быть прогнозов, получили %d", n)
	}
}

// --- T4: новости + сентимент в worker ---

// TestFetchNews_CoinPaprikaWritesItems — fetchOnce с фейковыми новостями CoinPaprika
// сохраняет их в news_items и связывает с монетой.
func TestFetchNews_CoinPaprikaWritesItems(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newWorkerDB(t)
	deps := newWorkerDeps(d)

	taker := fakeTaker{coins: []coingecko.MarketCoin{
		{ID: "bitcoin", CurrentPrice: 60000, LastUpdated: "2026-08-12T10:00:00Z"},
	}}
	news := fakeNewsTaker{news: []coinpaprika.RawNews{
		{ID: "cp-1", Title: "BTC rally", Summary: "Bitcoin up",
			URL: "https://cp.com/1", PublishedAt: "2026-08-12T10:00:00Z",
			RelatedCoins: []string{"btc-bitcoin"}},
	}}
	w := NewWithTakers(
		config.Config{FetchIntervalMin: 10, CoinPaprikaBaseURL: "https://fake.test/v1"},
		taker, news, fakeRSSTaker{},
		deps.assets, deps.sources, deps.prices, deps.logs,
		deps.indicators, deps.forecasts, deps.news, deps.sentiment,
		deps.outcomes, deps.factorStats,
	)

	w.fetchOnce(ctx)

	var n int
	if err := d.GetContext(ctx, &n, `SELECT COUNT(*) FROM news_items WHERE source_id=(SELECT id FROM sources WHERE slug='coinpaprika')`); err != nil {
		t.Fatalf("count news_items: %v", err)
	}
	if n != 1 {
		t.Errorf("хотели 1 новость CoinPaprika, получили %d", n)
	}

	// Новость связана с bitcoin (asset_id=1).
	var assetID *int64
	if err := d.QueryRowxContext(ctx,
		`SELECT asset_id FROM news_items WHERE external_id='cp-1'`).Scan(&assetID); err != nil {
		t.Fatalf("select news: %v", err)
	}
	if assetID == nil || *assetID != 1 {
		t.Errorf("хотели asset_id=1, получили %v", assetID)
	}
}

// TestFetchNews_RSSWritesItems — RSS-новости сохраняются без привязки к монете.
func TestFetchNews_RSSWritesItems(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newWorkerDB(t)
	deps := newWorkerDeps(d)

	taker := fakeTaker{coins: []coingecko.MarketCoin{
		{ID: "bitcoin", CurrentPrice: 60000, LastUpdated: "2026-08-12T10:00:00Z"},
	}}
	rssTaker := fakeRSSTaker{news: map[string][]rss.RawNews{
		rss.SlugCoindesk: {
			{ExternalID: "https://cd.com/1", Title: "Crypto news", Summary: "Big update",
				Link: "https://cd.com/1"},
		},
	}}
	w := NewWithTakers(
		config.Config{FetchIntervalMin: 10}, taker, fakeNewsTaker{}, rssTaker,
		deps.assets, deps.sources, deps.prices, deps.logs,
		deps.indicators, deps.forecasts, deps.news, deps.sentiment,
		deps.outcomes, deps.factorStats,
	)

	w.fetchOnce(ctx)

	var n int
	if err := d.GetContext(ctx, &n, `SELECT COUNT(*) FROM news_items WHERE source_id=(SELECT id FROM sources WHERE slug='rss-coindesk')`); err != nil {
		t.Fatalf("count news_items: %v", err)
	}
	if n != 1 {
		t.Errorf("хотели 1 RSS-новость, получили %d", n)
	}
}

// TestComputeForecasts_WithSentiment — при наличии оценённых новостей прогноз
// включает 4-й фактор sentiment.
func TestComputeForecasts_WithSentiment(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newWorkerDB(t)
	deps := newWorkerDeps(d)

	chart := buildMonotonicChart(25, 1723334400000)
	taker := fakeTaker{
		coins: []coingecko.MarketCoin{
			{ID: "bitcoin", Symbol: "btc", Name: "Bitcoin",
				CurrentPrice: 25, TotalVolume: 1000, LastUpdated: "2024-09-04T00:00:00Z"},
		},
		charts: map[string]coingecko.MarketChart{"bitcoin": chart},
	}
	w := newTestWorker(t, d, taker, deps)

	// Сначала индикаторы.
	w.fetchOnce(ctx)

	// Вставляем новость с позитивным сентиментом по bitcoin.
	assetID := int64(1)
	if _, err := deps.news.InsertMany(ctx, []model.NewsItem{{
		AssetID: &assetID, SourceID: 2, ExternalID: "sent-test",
		Title: "BTC soars", Body: "Bitcoin hits new high", Link: "https://x.com/1",
		PublishedAt: timeNowUTC(),
	}}); err != nil {
		t.Fatalf("вставка новости: %v", err)
	}
	// Проставляем сентимент.
	items, err := deps.news.Unscored(ctx, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("Unscored: err=%v len=%d", err, len(items))
	}
	if err := deps.news.SetSentiment(ctx, items[0].ID, 0.8, "очень позитивно"); err != nil {
		t.Fatalf("SetSentiment: %v", err)
	}

	w.computeForecasts(ctx)

	fc, factors, err := deps.forecasts.LatestByAsset(ctx, 1)
	if err != nil {
		t.Fatalf("прогноз: %v", err)
	}
	// Должно быть 4 фактора (sentiment добавлен).
	hasSentiment := false
	for _, f := range factors {
		if f.Name == string(scoring.FactorSentiment) {
			hasSentiment = true
			if f.Signal < 0.7 {
				t.Errorf("sentiment signal: хотели ≈0.8, получили %v", f.Signal)
			}
		}
	}
	if !hasSentiment {
		t.Errorf("хотели 4 фактора (с sentiment), получили %d: %+v", len(factors), factorNames(factors))
	}

	// Позитивный сентимент + монотонный рост → прогноз «вверх» с высокой уверенностью.
	if fc.Direction != "up" {
		t.Errorf("хотели up, получили %s", fc.Direction)
	}
}

// factorNames — список имён факторов для отладочного вывода.
func factorNames(factors []model.ForecastFactor) []string {
	names := make([]string, len(factors))
	for i, f := range factors {
		names[i] = f.Name
	}
	return names
}

// timeNowUTC — текущий момент в UTC (для published_at в тестах).
func timeNowUTC() (t time.Time) { return time.Now().UTC() }

// --- T5: resolve-цикл и адаптация весов ---

// TestResolveOnce_CreatesOutcomeAndUpdatesStats — прогноз старше 24ч сверен с
// фактом: создаётся outcome и обновляется factor_stats.
func TestResolveOnce_CreatesOutcomeAndUpdatesStats(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newWorkerDB(t)
	deps := newWorkerDeps(d)

	chart := buildMonotonicChart(25, 1723334400000)
	taker := fakeTaker{
		coins: []coingecko.MarketCoin{
			{ID: "bitcoin", Symbol: "btc", Name: "Bitcoin",
				CurrentPrice: 25, TotalVolume: 1000, LastUpdated: "2024-09-04T00:00:00Z"},
		},
		charts: map[string]coingecko.MarketChart{"bitcoin": chart},
	}
	w := newTestWorker(t, d, taker, deps)

	// Сначала индикаторы + прогноз.
	w.fetchOnce(ctx)
	w.computeForecasts(ctx)

	// Делаем прогноз «старым» — сдвигаем created_at на 25ч назад.
	_, err := d.ExecContext(ctx, `UPDATE forecasts SET created_at = ? WHERE status = ?`,
		time.Now().UTC().Add(-25*time.Hour), string(model.ForecastStatusActive))
	if err != nil {
		t.Fatalf("сдвиг created_at: %v", err)
	}

	// Добавляем «текущую» цену (resolution) — выше цены на момент прогноза,
	// чтобы прогноз «вверх» получил hit и factor_stats обновился.
	assetID := int64(1)
	srcID := int64(1)
	_, err = d.ExecContext(ctx,
		`INSERT OR IGNORE INTO price_points (asset_id, ts, price_usd, market_cap, volume, source_id, change_24h)
		 VALUES (?, ?, ?, 0, 0, ?, 0)`,
		assetID, time.Now().UTC(), 28, srcID)
	if err != nil {
		t.Fatalf("вставка resolution-цены: %v", err)
	}

	// Запускаем resolve.
	w.resolveOnce(ctx)

	// Должен появиться outcome.
	var nOutcomes int
	if err := d.GetContext(ctx, &nOutcomes, `SELECT COUNT(*) FROM outcomes`); err != nil {
		t.Fatalf("count outcomes: %v", err)
	}
	if nOutcomes != 1 {
		t.Fatalf("хотели 1 outcome, получили %d", nOutcomes)
	}

	// Результат — hit (прогноз вверх, цена выросла 25→28 = +12%).
	var result string
	if err := d.GetContext(ctx, &result, `SELECT result FROM outcomes LIMIT 1`); err != nil {
		t.Fatalf("select result: %v", err)
	}
	if model.OutcomeResult(result) != model.OutcomeHit {
		t.Errorf("хотели result=hit, получили %s", result)
	}

	// Прогноз переведён в resolved.
	var status string
	if err := d.GetContext(ctx, &status, `SELECT status FROM forecasts WHERE id = (SELECT forecast_id FROM outcomes LIMIT 1)`); err != nil {
		t.Fatalf("select forecast status: %v", err)
	}
	if model.ForecastStatus(status) != model.ForecastStatusResolved {
		t.Errorf("хотели status=resolved, получили %s", status)
	}

	// factor_stats должен иметь записи для каждого фактора прогноза (3 фактора
	// без sentiment — новостей нет).
	var nStats int
	if err := d.GetContext(ctx, &nStats, `SELECT COUNT(*) FROM factor_stats WHERE asset_id = 1`); err != nil {
		t.Fatalf("count factor_stats: %v", err)
	}
	if nStats != 3 {
		t.Errorf("хотели 3 factor_stats (3 фактора), получили %d", nStats)
	}
}

// TestAdaptedWeights_FactorStatsAffectsForecast — при наличии factor_stats с
// низкой hit_rate_ema вес фактора понижается, что видно в декомпозиции прогноза.
func TestAdaptedWeights_FactorStatsAffectsForecast(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newWorkerDB(t)
	deps := newWorkerDeps(d)

	chart := buildMonotonicChart(25, 1723334400000)
	taker := fakeTaker{
		coins: []coingecko.MarketCoin{
			{ID: "bitcoin", Symbol: "btc", Name: "Bitcoin",
				CurrentPrice: 25, TotalVolume: 1000, LastUpdated: "2024-09-04T00:00:00Z"},
		},
		charts: map[string]coingecko.MarketChart{"bitcoin": chart},
	}
	w := newTestWorker(t, d, taker, deps)

	// Без статистики — прогноз с дефолтными весами.
	w.fetchOnce(ctx)
	w.computeForecasts(ctx)

	_, factorsBefore, err := deps.forecasts.LatestByAsset(ctx, 1)
	if err != nil {
		t.Fatalf("прогноз (до): %v", err)
	}

	// Устанавливаем factor_stats: momentum имеет низкий hit_rate (0.1) → понижение.
	lowEMA := 0.1
	if err := deps.factorStats.Upsert(ctx, model.FactorStat{
		AssetID: 1, Factor: string(scoring.FactorMomentum),
		HitRateEMA: lowEMA, Samples: 10, UpdatedAt: timeNowUTC(),
	}); err != nil {
		t.Fatalf("factor_stats upsert: %v", err)
	}

	// Пересчитываем прогноз — momentum должен получить пониженный вес.
	// Переводим старый active в superseded, чтобы Save создал новый active.
	w.computeForecasts(ctx)

	_, factorsAfter, err := deps.forecasts.LatestByAsset(ctx, 1)
	if err != nil {
		t.Fatalf("прогноз (после): %v", err)
	}

	// Находим momentum до и после.
	var momWeightBefore, momWeightAfter float64
	for _, f := range factorsBefore {
		if f.Name == string(scoring.FactorMomentum) {
			momWeightBefore = f.BaseWeight
		}
	}
	for _, f := range factorsAfter {
		if f.Name == string(scoring.FactorMomentum) {
			momWeightAfter = f.BaseWeight
		}
	}
	// BaseWeight в forecast_factors хранит уже адаптированный вес (переданный
	// в scoring как weights). При EMA=0.1 множитель = clamp(0.1/0.5,0.5,1.5)=0.5.
	if momWeightAfter >= momWeightBefore {
		t.Errorf("при низкой hit_rate вес momentum должен понизиться: до=%v, после=%v",
			momWeightBefore, momWeightAfter)
	}
}
