package worker

import (
	"context"
	"testing"

	"github.com/jmoiron/sqlx"

	"test-future/internal/config"
	"test-future/internal/db"
	"test-future/internal/model"
	"test-future/internal/source/coingecko"
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

// TestFetchOnce_WritesPointsAndLog — один цикл worker: тянет цены, пишет точки
// и успешную запись в update_log. Проверяем сквозной путь источник→БД.
func TestFetchOnce_WritesPointsAndLog(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newWorkerDB(t)

	assetsRepo := storage.NewAssets(d)
	sourcesRepo := storage.NewSources(d)
	priceRepo := storage.NewPricePoints(d)
	logRepo := storage.NewUpdateLog(d)
	indRepo := storage.NewIndicatorSnapshots(d)

	cfg := config.Config{FetchIntervalMin: 10}
	taker := fakeTaker{coins: []coingecko.MarketCoin{
		{ID: "bitcoin", Symbol: "btc", Name: "Bitcoin",
			CurrentPrice: 60000, MarketCap: 1e12, TotalVolume: 3e10,
			PriceChangePercentage24H: 1.5, LastUpdated: "2026-08-12T10:00:00Z"},
		{ID: "ethereum", Symbol: "eth", Name: "Ethereum",
			CurrentPrice: 3000, MarketCap: 3.6e11, TotalVolume: 1.5e10,
			PriceChangePercentage24H: -0.5, LastUpdated: "2026-08-12T10:00:00Z"},
	}}
	w := NewWithTaker(cfg, taker, assetsRepo, sourcesRepo, priceRepo, logRepo, indRepo, storage.NewForecasts(d))

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

	w := NewWithTaker(
		config.Config{FetchIntervalMin: 10},
		fakeTaker{err: errFake("источник недоступен")},
		storage.NewAssets(d),
		storage.NewSources(d),
		storage.NewPricePoints(d),
		storage.NewUpdateLog(d),
		storage.NewIndicatorSnapshots(d),
		storage.NewForecasts(d),
	)

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

	w := NewWithTaker(
		config.Config{FetchIntervalMin: 10},
		fakeTaker{coins: []coingecko.MarketCoin{
			{ID: "bitcoin", CurrentPrice: 60000, LastUpdated: "2026-08-12T10:00:00Z"},
		}},
		storage.NewAssets(d),
		storage.NewSources(d),
		storage.NewPricePoints(d),
		storage.NewUpdateLog(d),
		storage.NewIndicatorSnapshots(d),
		storage.NewForecasts(d),
	)

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

	assetsRepo := storage.NewAssets(d)
	sourcesRepo := storage.NewSources(d)
	priceRepo := storage.NewPricePoints(d)
	logRepo := storage.NewUpdateLog(d)
	indRepo := storage.NewIndicatorSnapshots(d)

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
	w := NewWithTaker(config.Config{FetchIntervalMin: 10}, taker,
		assetsRepo, sourcesRepo, priceRepo, logRepo, indRepo, storage.NewForecasts(d))

	w.fetchOnce(ctx)

	snap, err := indRepo.ByAsset(ctx, 1) // bitcoin → asset_id=1 из сидинга
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

	indRepo := storage.NewIndicatorSnapshots(d)
	// Ряд из 5 точек — меньше rocPeriod(10)+1=11, расчёт пропускается.
	chart := buildMonotonicChart(5, 1723334400000)
	taker := fakeTaker{
		coins: []coingecko.MarketCoin{
			{ID: "bitcoin", CurrentPrice: 5, LastUpdated: "2026-08-12T10:00:00Z"},
		},
		charts: map[string]coingecko.MarketChart{"bitcoin": chart},
	}
	w := NewWithTaker(config.Config{FetchIntervalMin: 10}, taker,
		storage.NewAssets(d), storage.NewSources(d),
		storage.NewPricePoints(d), storage.NewUpdateLog(d), indRepo, storage.NewForecasts(d))

	w.fetchOnce(ctx)

	// Снапшота нет — цикл корректно пропустил расчёт.
	if _, err := indRepo.ByAsset(ctx, 1); err == nil {
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

	forecastRepo := storage.NewForecasts(d)
	chart := buildMonotonicChart(25, 1723334400000)
	taker := fakeTaker{
		coins: []coingecko.MarketCoin{
			{ID: "bitcoin", Symbol: "btc", Name: "Bitcoin",
				CurrentPrice: 25, TotalVolume: 1000, LastUpdated: "2024-09-04T00:00:00Z"},
		},
		charts: map[string]coingecko.MarketChart{"bitcoin": chart},
	}
	w := NewWithTaker(config.Config{FetchIntervalMin: 10}, taker,
		storage.NewAssets(d), storage.NewSources(d),
		storage.NewPricePoints(d), storage.NewUpdateLog(d),
		storage.NewIndicatorSnapshots(d), forecastRepo)

	// Сначала цикл цен/индикаторов, затем — прогнозов.
	w.fetchOnce(ctx)
	w.computeForecasts(ctx)

	// Должен появиться активный прогноз по bitcoin (asset_id=1).
	fc, factors, err := forecastRepo.LatestByAsset(ctx, 1)
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
	if len(factors) != 3 {
		t.Errorf("хотели 3 фактора, получили %d", len(factors))
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

	w := NewWithTaker(config.Config{FetchIntervalMin: 10},
		fakeTaker{coins: []coingecko.MarketCoin{
			{ID: "bitcoin", CurrentPrice: 60000, LastUpdated: "2026-08-12T10:00:00Z"},
		}},
		storage.NewAssets(d), storage.NewSources(d),
		storage.NewPricePoints(d), storage.NewUpdateLog(d),
		storage.NewIndicatorSnapshots(d), storage.NewForecasts(d))

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
