package storage

import (
	"context"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"test-future/internal/db"
	"test-future/internal/model"
)

// newTestDB открывает in-memory SQLite, применяет схему и сидирует справочники.
// Возвращает готовое соединение и идентификаторы для тестов.
func newTestDB(t *testing.T) *sqlx.DB {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("открытие тестовой БД: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// TestInsertOne_DedupByKey — повторная вставка той же (asset_id, ts, source_id)
// игнорируется (возвращает false), не создавая дублей.
func TestInsertOne_DedupByKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newTestDB(t)
	repo := NewPricePoints(d)

	ts := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	pp := model.PricePoint{
		AssetID:   1, // bitcoin из сидинга
		TS:        ts,
		PriceUSD:  60000,
		SourceID:  1, // coingecko из сидинга
		Change24H: 0.01,
	}

	// Первая вставка — новая.
	added, err := repo.InsertOne(ctx, pp)
	if err != nil {
		t.Fatalf("первая вставка: %v", err)
	}
	if !added {
		t.Fatal("первая вставка должна добавить строку")
	}

	// Повтор — должен быть проигнорирован (дедуп).
	pp.PriceUSD = 99999 // изменили значение, но ключ тот же
	added, err = repo.InsertOne(ctx, pp)
	if err != nil {
		t.Fatalf("повторная вставка: %v", err)
	}
	if added {
		t.Fatal("повторная вставка не должна добавлять строку (дедуп)")
	}

	// В таблице ровно одна строка для этого ключа.
	var n int
	if err := d.GetContext(ctx, &n,
		`SELECT COUNT(*) FROM price_points WHERE asset_id=1 AND ts=? AND source_id=1`, ts); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("хотели 1 строку после дедупа, получили %d", n)
	}
}

// TestInsertOne_DifferentKeysKept — разные ts или source_id дают разные строки.
func TestInsertOne_DifferentKeysKept(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newTestDB(t)
	repo := NewPricePoints(d)

	base := model.PricePoint{AssetID: 1, TS: time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC), PriceUSD: 60000, SourceID: 1}

	// Тот же asset + source, но другой ts — отдельная точка.
	other := base
	other.TS = base.TS.Add(10 * time.Minute)
	other.PriceUSD = 60500

	if added, err := repo.InsertOne(ctx, base); err != nil || !added {
		t.Fatalf("base вставка: added=%v err=%v", added, err)
	}
	if added, err := repo.InsertOne(ctx, other); err != nil || !added {
		t.Fatalf("other вставка: added=%v err=%v", added, err)
	}

	items, err := repo.LatestByAsset(ctx)
	if err != nil {
		t.Fatalf("LatestByAsset: %v", err)
	}
	// 5 ассетов из сидинга, но цены есть только у одного (asset_id=1).
	var btc *model.AssetPrice
	for i := range items {
		if items[i].CoinID == "bitcoin" {
			btc = &items[i]
		}
	}
	if btc == nil {
		t.Fatal("не нашли bitcoin в ответе")
	}
	if btc.PriceUSD != 60500 {
		t.Errorf("последняя цена bitcoin: хотели 60500, получили %v", btc.PriceUSD)
	}
}

// TestLatestByAsset_AssetWithoutPrice — актив без цен отдаётся с нулями
// (LEFT JOIN), не выпадает из списка.
func TestLatestByAsset_AssetWithoutPrice(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newTestDB(t)
	repo := NewPricePoints(d)

	items, err := repo.LatestByAsset(ctx)
	if err != nil {
		t.Fatalf("LatestByAsset: %v", err)
	}
	if len(items) != 5 {
		t.Fatalf("хотели 5 ассетов, получили %d", len(items))
	}
	for _, ap := range items {
		if ap.PriceUSD != 0 {
			t.Errorf("ассет %s без цен должен иметь price_usd=0, получили %v", ap.CoinID, ap.PriceUSD)
		}
	}
}
