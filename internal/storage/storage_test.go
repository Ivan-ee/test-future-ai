package storage

import (
	"context"
	"math"
	"strconv"
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

// insertPricePoint — вспомогательный помощник: вставляет точку цены и падает при ошибке.
func insertPricePoint(t *testing.T, ctx context.Context, repo *PricePoints, assetID int64, ts time.Time, price, volume float64) {
	t.Helper()
	_, err := repo.InsertOne(ctx, model.PricePoint{
		AssetID:  assetID,
		TS:       ts,
		PriceUSD: price,
		Volume:   volume,
		SourceID: 1, // coingecko из сидинга
	})
	if err != nil {
		t.Fatalf("вставка price_point: %v", err)
	}
}

// TestLastClosesByAsset_OrderedAscending — последние n цен по возрастанию ts.
func TestLastClosesByAsset_OrderedAscending(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newTestDB(t)
	repo := NewPricePoints(d)

	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	for i := range 5 {
		insertPricePoint(t, ctx, repo, 1, base.AddDate(0, 0, i), float64(100+i*10), 1000)
	}

	closes, err := repo.LastClosesByAsset(ctx, 1, 3)
	if err != nil {
		t.Fatalf("LastClosesByAsset: %v", err)
	}
	if len(closes) != 3 {
		t.Fatalf("хотели 3 точки, получили %d", len(closes))
	}
	// Последние 3 по возрастанию: 120, 130, 140.
	want := []float64{120, 130, 140}
	for i, w := range want {
		if closes[i] != w {
			t.Errorf("closes[%d]: хотели %v, получили %v", i, w, closes[i])
		}
	}
}

// TestLastVolumesByAsset_OrderedAscending — последние n объёмов по возрастанию ts.
func TestLastVolumesByAsset_OrderedAscending(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newTestDB(t)
	repo := NewPricePoints(d)

	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	for i := range 4 {
		insertPricePoint(t, ctx, repo, 1, base.AddDate(0, 0, i), 100, float64(1000+i*500))
	}

	volumes, err := repo.LastVolumesByAsset(ctx, 1, 2)
	if err != nil {
		t.Fatalf("LastVolumesByAsset: %v", err)
	}
	if len(volumes) != 2 {
		t.Fatalf("хотели 2 точки, получили %d", len(volumes))
	}
	want := []float64{2000, 2500} // последние 2 по возрастанию
	for i, w := range want {
		if volumes[i] != w {
			t.Errorf("volumes[%d]: хотели %v, получили %v", i, w, volumes[i])
		}
	}
}

// TestLastClosesByAsset_DedupByTimestamp — несколько источников на один ts
// дают одну цену (минимальный source_id), не дублируют ряд.
func TestLastClosesByAsset_DedupByTimestamp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newTestDB(t)
	repo := NewPricePoints(d)

	// Добавляем второй источник, чтобы не нарушить foreign key на source_id=2.
	if _, err := d.ExecContext(ctx,
		`INSERT INTO sources (slug, name) VALUES ('test2', 'Test Source 2')`); err != nil {
		t.Fatalf("insert source: %v", err)
	}

	ts := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	// Две точки на один ts от разных источников.
	if _, err := d.ExecContext(ctx,
		`INSERT INTO price_points (asset_id, ts, price_usd, volume, source_id) VALUES (?, ?, ?, ?, ?)`,
		1, ts, 100, 1000, 1); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := d.ExecContext(ctx,
		`INSERT INTO price_points (asset_id, ts, price_usd, volume, source_id) VALUES (?, ?, ?, ?, ?)`,
		1, ts, 999, 9999, 2); err != nil { // дубль по ts, другой source
		t.Fatalf("insert: %v", err)
	}

	closes, err := repo.LastClosesByAsset(ctx, 1, 5)
	if err != nil {
		t.Fatalf("LastClosesByAsset: %v", err)
	}
	if len(closes) != 1 {
		t.Fatalf("дедуп: хотели 1 точку, получили %d", len(closes))
	}
	if closes[0] != 100 { // минимальный source_id=1 → цена 100
		t.Errorf("хотели цену 100 (source_id=1), получили %v", closes[0])
	}
}

// TestIndicatorSnapshots_UpsertThenByAsset — UPSERT создаёт, затем обновляет
// строку; ByAsset возвращает актуальные значения.
func TestIndicatorSnapshots_UpsertThenByAsset(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newTestDB(t)
	repo := NewIndicatorSnapshots(d)

	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC).UTC()
	first := model.IndicatorSnapshot{
		AssetID: 1, SourceID: 1,
		RSI: 55, ROC: 2.1, SMA7: 60000, SMA20: 59000, VolumeSignal: 1.2,
		CalculatedAt: now,
	}
	if err := repo.Upsert(ctx, first); err != nil {
		t.Fatalf("первый upsert: %v", err)
	}

	got, err := repo.ByAsset(ctx, 1)
	if err != nil {
		t.Fatalf("ByAsset: %v", err)
	}
	if got.RSI != 55 {
		t.Errorf("RSI: хотели 55, получили %v", got.RSI)
	}

	// Повторный upsert — обновляет значения.
	updated := first
	updated.RSI = 72.5
	if err := repo.Upsert(ctx, updated); err != nil {
		t.Fatalf("повторный upsert: %v", err)
	}
	got, err = repo.ByAsset(ctx, 1)
	if err != nil {
		t.Fatalf("ByAsset после обновления: %v", err)
	}
	if got.RSI != 72.5 {
		t.Errorf("RSI после upsert: хотели 72.5, получили %v", got.RSI)
	}

	// В таблице ровно одна строка на актив.
	var n int
	if err := d.GetContext(ctx, &n, `SELECT COUNT(*) FROM indicator_snapshots WHERE asset_id=1`); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("хотели 1 строку после upsert, получили %d", n)
	}
}

// TestIndicatorSnapshots_ByAssetNotFound — нет данных → sql.ErrNoRows.
func TestIndicatorSnapshots_ByAssetNotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newTestDB(t)
	repo := NewIndicatorSnapshots(d)

	_, err := repo.ByAsset(ctx, 999)
	if err == nil {
		t.Fatal("ожидали ошибку для несуществующего снапшота")
	}
}

// --- T4: репозиторий NewsItems ---

// makeNewsItem — помощник: создаёт NewsItem с заданным external_id и asset_id=1.
func makeNewsItem(externalID string, assetID int64) model.NewsItem {
	var aid *int64
	if assetID > 0 {
		aid = &assetID
	}
	pub := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	return model.NewsItem{
		AssetID:     aid,
		SourceID:    2, // coinpaprika из сидинга
		ExternalID:  externalID,
		Title:       "Test news " + externalID,
		Body:        "Body of " + externalID,
		Link:        "https://example.com/" + externalID,
		PublishedAt: pub,
	}
}

// TestNewsItems_InsertMany_Dedup — повторная вставка той же новости (source_id +
// external_id) игнорируется.
func TestNewsItems_InsertMany_Dedup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newTestDB(t)
	repo := NewNewsItems(d)

	items := []model.NewsItem{makeNewsItem("n1", 1), makeNewsItem("n2", 1)}

	added, err := repo.InsertMany(ctx, items)
	if err != nil {
		t.Fatalf("первая вставка: %v", err)
	}
	if added != 2 {
		t.Fatalf("хотели 2 добавленных, получили %d", added)
	}

	// Повтор — дедуп.
	added, err = repo.InsertMany(ctx, items)
	if err != nil {
		t.Fatalf("повторная вставка: %v", err)
	}
	if added != 0 {
		t.Errorf("повтор: хотели 0 добавленных (дедуп), получили %d", added)
	}

	var n int
	if err := d.GetContext(ctx, &n, `SELECT COUNT(*) FROM news_items`); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Errorf("хотели 2 строки, получили %d", n)
	}
}

// TestNewsItems_Unscored — неотсентиченные новости выбираются по ORDER BY inserted_at.
func TestNewsItems_Unscored(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newTestDB(t)
	repo := NewNewsItems(d)

	if _, err := repo.InsertMany(ctx, []model.NewsItem{
		makeNewsItem("n1", 1), makeNewsItem("n2", 1), makeNewsItem("n3", 1),
	}); err != nil {
		t.Fatalf("вставка: %v", err)
	}

	// Сентимим одну — она не должна попасть в Unscored.
	if err := repo.SetSentiment(ctx, 1, 0.5, "позитив"); err != nil {
		t.Fatalf("SetSentiment: %v", err)
	}

	items, err := repo.Unscored(ctx, 10)
	if err != nil {
		t.Fatalf("Unscored: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("хотели 2 неотсентиченные, получили %d", len(items))
	}
}

// TestNewsItems_SetSentiment_ClampsScore — score зажимается в [-1..1].
func TestNewsItems_SetSentiment_ClampsScore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newTestDB(t)
	repo := NewNewsItems(d)

	if _, err := repo.InsertMany(ctx, []model.NewsItem{makeNewsItem("n1", 1)}); err != nil {
		t.Fatalf("вставка: %v", err)
	}

	if err := repo.SetSentiment(ctx, 1, 5.0, "слишком позитивно"); err != nil {
		t.Fatalf("SetSentiment: %v", err)
	}

	var score float64
	if err := d.GetContext(ctx, &score, `SELECT sentiment_score FROM news_items WHERE id=1`); err != nil {
		t.Fatalf("select score: %v", err)
	}
	if score != 1.0 {
		t.Errorf("хотели score зажатый до 1.0, получили %v", score)
	}
}

// TestNewsItems_RecentByAsset — последние новости по монете за период, по убыванию даты.
func TestNewsItems_RecentByAsset(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newTestDB(t)
	repo := NewNewsItems(d)

	base := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	items := make([]model.NewsItem, 3)
	for i := range 3 {
		n := makeNewsItem("n"+strconv.Itoa(i), 1)
		n.PublishedAt = base.Add(time.Duration(i) * time.Hour)
		items[i] = n
	}
	if _, err := repo.InsertMany(ctx, items); err != nil {
		t.Fatalf("вставка: %v", err)
	}

	got, err := repo.RecentByAsset(ctx, 1, base, 10)
	if err != nil {
		t.Fatalf("RecentByAsset: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("хотели 3 новости, получили %d", len(got))
	}
	// По убыванию: n2 (последняя по времени) → первая.
	if got[0].ExternalID != "n2" {
		t.Errorf("первая новость: хотели n2, получили %s", got[0].ExternalID)
	}
}

// TestNewsItems_RecentByAsset_FiltersBySince — новости старше since не возвращаются.
func TestNewsItems_RecentByAsset_FiltersBySince(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newTestDB(t)
	repo := NewNewsItems(d)

	base := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	items := []model.NewsItem{
		func() model.NewsItem { n := makeNewsItem("old", 1); n.PublishedAt = base.Add(-2 * time.Hour); return n }(),
		func() model.NewsItem { n := makeNewsItem("new", 1); n.PublishedAt = base; return n }(),
	}
	if _, err := repo.InsertMany(ctx, items); err != nil {
		t.Fatalf("вставка: %v", err)
	}

	got, err := repo.RecentByAsset(ctx, 1, base.Add(-1*time.Hour), 10)
	if err != nil {
		t.Fatalf("RecentByAsset: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("хотели 1 новость (моложе since), получили %d", len(got))
	}
	if got[0].ExternalID != "new" {
		t.Errorf("хотели news=new, получили %s", got[0].ExternalID)
	}
}

// TestNewsItems_AvgSentimentByAsset — средний сентимент по новостям с проставленным score.
// _nil если нет оценённых новостей.
func TestNewsItems_AvgSentimentByAsset(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newTestDB(t)
	repo := NewNewsItems(d)

	if _, err := repo.InsertMany(ctx, []model.NewsItem{
		makeNewsItem("n1", 1), makeNewsItem("n2", 1), makeNewsItem("n3", 1),
	}); err != nil {
		t.Fatalf("вставка: %v", err)
	}

	// Нет оценённых → nil.
	avg, err := repo.AvgSentimentByAsset(ctx, 1, time.Time{})
	if err != nil {
		t.Fatalf("AvgSentiment (нет оценённых): %v", err)
	}
	if avg != nil {
		t.Errorf("хотели nil, получили %v", *avg)
	}

	// Оцениваем две: (0.6 + (-0.2)) / 2 = 0.2.
	if err := repo.SetSentiment(ctx, 1, 0.6, ""); err != nil {
		t.Fatalf("SetSentiment 1: %v", err)
	}
	if err := repo.SetSentiment(ctx, 2, -0.2, ""); err != nil {
		t.Fatalf("SetSentiment 2: %v", err)
	}

	avg, err = repo.AvgSentimentByAsset(ctx, 1, time.Time{})
	if err != nil {
		t.Fatalf("AvgSentiment: %v", err)
	}
	if avg == nil {
		t.Fatal("ожидали ненулевой avg")
	}
	if math.Abs(*avg-0.2) > 1e-9 {
		t.Errorf("хотели avg=0.2, получили %v", *avg)
	}
}

// TestNewsItems_NullAssetID — новость без связи с монетой (asset_id=nil) корректно
// сохраняется и читается.
func TestNewsItems_NullAssetID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newTestDB(t)
	repo := NewNewsItems(d)

	n := makeNewsItem("general", 0) // assetID=0 → nil
	if _, err := repo.InsertMany(ctx, []model.NewsItem{n}); err != nil {
		t.Fatalf("вставка: %v", err)
	}

	got, err := repo.Unscored(ctx, 10)
	if err != nil {
		t.Fatalf("Unscored: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("хотели 1 новость, получили %d", len(got))
	}
	if got[0].AssetID != nil {
		t.Errorf("хотели nil AssetID, получили %v", *got[0].AssetID)
	}
}
