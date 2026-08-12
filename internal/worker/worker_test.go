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

// fakeTaker — тестовый источник CoinGecko: отдаёт заранее заданные монеты.
type fakeTaker struct {
	coins []coingecko.MarketCoin
	err   error
}

func (f fakeTaker) Markets(_ context.Context, _ []string) ([]coingecko.MarketCoin, error) {
	return f.coins, f.err
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

	cfg := config.Config{FetchIntervalMin: 10}
	taker := fakeTaker{coins: []coingecko.MarketCoin{
		{ID: "bitcoin", Symbol: "btc", Name: "Bitcoin",
			CurrentPrice: 60000, MarketCap: 1e12, TotalVolume: 3e10,
			PriceChangePercentage24H: 1.5, LastUpdated: "2026-08-12T10:00:00Z"},
		{ID: "ethereum", Symbol: "eth", Name: "Ethereum",
			CurrentPrice: 3000, MarketCap: 3.6e11, TotalVolume: 1.5e10,
			PriceChangePercentage24H: -0.5, LastUpdated: "2026-08-12T10:00:00Z"},
	}}
	w := NewWithTaker(cfg, taker, assetsRepo, sourcesRepo, priceRepo, logRepo)

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
