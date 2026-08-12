package coinpaprika

import (
	"testing"
	"time"

	"test-future/internal/model"
)

// TestNewsMap_RelatesByCoinSlug — новость с RelatedCoins связывается с монетой
// по слагу CoinPaprika → coin_id CoinGecko.
func TestNewsMap_RelatesByCoinSlug(t *testing.T) {
	t.Parallel()
	assets := map[string]model.Asset{
		"bitcoin":  {ID: 1, CoinID: "bitcoin"},
		"ethereum": {ID: 2, CoinID: "ethereum"},
	}
	raw := []RawNews{
		{
			ID:           "n1",
			Title:        "BTC rallies",
			Summary:      "Bitcoin surges past 60k",
			URL:          "https://example.com/n1",
			PublishedAt:  "2026-08-12T10:00:00Z",
			RelatedCoins: []string{"btc-bitcoin"},
		},
		{
			ID:           "n2",
			Title:        "ETH upgrade",
			Summary:      "Ethereum completes upgrade",
			URL:          "https://example.com/n2",
			PublishedAt:  "2026-08-12T11:00:00Z",
			RelatedCoins: []string{"eth-ethereum"},
		},
	}

	items := NewsMap(raw, assets, 10)
	if len(items) != 2 {
		t.Fatalf("хотели 2 новости, получили %d", len(items))
	}
	if items[0].AssetID == nil || *items[0].AssetID != 1 {
		t.Errorf("n1: хотели asset_id=1 (bitcoin), получили %v", items[0].AssetID)
	}
	if items[1].AssetID == nil || *items[1].AssetID != 2 {
		t.Errorf("n2: хотели asset_id=2 (ethereum), получили %v", items[1].AssetID)
	}
	if items[0].SourceID != 10 {
		t.Errorf("хотели source_id=10, получили %d", items[0].SourceID)
	}
	if items[0].Title != "BTC rallies" {
		t.Errorf("title: получили %q", items[0].Title)
	}
	if items[0].Body != "Bitcoin surges past 60k" {
		t.Errorf("body: получили %q", items[0].Body)
	}
}

// TestNewsMap_NoRelatedCoins — новость без RelatedCoins → asset_id=nil.
func TestNewsMap_NoRelatedCoins(t *testing.T) {
	t.Parallel()
	assets := map[string]model.Asset{"bitcoin": {ID: 1, CoinID: "bitcoin"}}
	raw := []RawNews{
		{ID: "n1", Title: "Crypto regulation", PublishedAt: "2026-08-12T10:00:00Z"},
	}

	items := NewsMap(raw, assets, 10)
	if len(items) != 1 {
		t.Fatalf("хотели 1 новость, получили %d", len(items))
	}
	if items[0].AssetID != nil {
		t.Errorf("хотели nil AssetID, получили %v", *items[0].AssetID)
	}
}

// TestNewsMap_UnknownSlug — новость со слагом монеты не из нашего списка → nil.
func TestNewsMap_UnknownSlug(t *testing.T) {
	t.Parallel()
	assets := map[string]model.Asset{"bitcoin": {ID: 1, CoinID: "bitcoin"}}
	raw := []RawNews{
		{
			ID:           "n1",
			PublishedAt:  "2026-08-12T10:00:00Z",
			RelatedCoins: []string{"dogecoin-doge"}, // не отслеживаем
		},
	}

	items := NewsMap(raw, assets, 10)
	if len(items) != 1 {
		t.Fatalf("хотели 1 новость, получили %d", len(items))
	}
	if items[0].AssetID != nil {
		t.Errorf("хотели nil для неизвестного слага, получили %v", *items[0].AssetID)
	}
}

// TestNewsMap_InvalidTimeFallsBackToNow — при невалидной дате берётся now (не zero).
func TestNewsMap_InvalidTimeFallsBackToNow(t *testing.T) {
	t.Parallel()
	raw := []RawNews{{ID: "n1", PublishedAt: "not-a-date"}}
	items := NewsMap(raw, map[string]model.Asset{}, 1)
	if len(items) != 1 {
		t.Fatalf("хотели 1 новость, получили %d", len(items))
	}
	if items[0].PublishedAt.IsZero() {
		t.Error("ожидали fallback на now, получили zero time")
	}
	// Должно быть в пределах последних 10 секунд.
	if time.Since(items[0].PublishedAt) > 10*time.Second {
		t.Error("published_at слишком далеко от now")
	}
}

// TestNewsMap_EmptyInput — пустой вход → пустой выход.
func TestNewsMap_EmptyInput(t *testing.T) {
	t.Parallel()
	items := NewsMap(nil, map[string]model.Asset{}, 1)
	if len(items) != 0 {
		t.Errorf("хотели 0 новостей, получили %d", len(items))
	}
}

// TestSlugsForCoinIDs — маппинг coin_id CoinGecko → слаги CoinPaprika.
func TestSlugsForCoinIDs(t *testing.T) {
	t.Parallel()
	coinIDs := []string{"bitcoin", "ethereum", "unknown-coin"}
	slugs := SlugsForCoinIDs(coinIDs)
	if len(slugs) != 2 {
		t.Fatalf("хотели 2 слага (unknown пропущен), получили %d: %v", len(slugs), slugs)
	}
	// Проверяем что оба ожидаемых слага есть.
	expect := map[string]bool{"btc-bitcoin": false, "eth-ethereum": false}
	for _, s := range slugs {
		expect[s] = true
	}
	for slug, found := range expect {
		if !found {
			t.Errorf("слаг %s отсутствует в результате: %v", slug, slugs)
		}
	}
}

// TestSlugsForCoinIDs_Empty — пустой вход → пустой выход.
func TestSlugsForCoinIDs_Empty(t *testing.T) {
	t.Parallel()
	slugs := SlugsForCoinIDs(nil)
	if len(slugs) != 0 {
		t.Errorf("хотели 0, получили %d", len(slugs))
	}
}
