package coingecko

import (
	"testing"
	"time"

	"test-future/internal/model"
)

// TestMarketMap_HappyPath — корректный маппинг ответа API в точки цен.
// Проверяем все поля и нормализацию процентов в доли.
func TestMarketMap_HappyPath(t *testing.T) {
	t.Parallel()
	assets := map[string]model.Asset{
		"bitcoin":  {ID: 1, CoinID: "bitcoin", Symbol: "BTC", Name: "Bitcoin"},
		"ethereum": {ID: 2, CoinID: "ethereum", Symbol: "ETH", Name: "Ethereum"},
	}
	coins := []MarketCoin{
		{
			ID:                       "bitcoin",
			Symbol:                   "btc",
			Name:                     "Bitcoin",
			CurrentPrice:             60000.5,
			MarketCap:                1_200_000_000_000,
			TotalVolume:              30_000_000_000,
			PriceChangePercentage24H: 2.5, // проценты
			LastUpdated:              "2026-08-12T10:00:00Z",
		},
		{
			ID:                       "ethereum",
			Symbol:                   "eth",
			Name:                     "Ethereum",
			CurrentPrice:             3000.0,
			MarketCap:                360_000_000_000,
			TotalVolume:              15_000_000_000,
			PriceChangePercentage24H: -1.25, // проценты
			LastUpdated:              "2026-08-12T10:00:00Z",
		},
	}

	points := MarketMap(coins, assets, int64(99))
	if len(points) != 2 {
		t.Fatalf("хотели 2 точки, получили %d", len(points))
	}

	// Порядок точек соответствует порядку монет в ответе.
	btc := points[0]
	if btc.AssetID != 1 {
		t.Errorf("AssetID: хотели 1, получили %d", btc.AssetID)
	}
	if btc.PriceUSD != 60000.5 {
		t.Errorf("PriceUSD: хотели 60000.5, получили %v", btc.PriceUSD)
	}
	if btc.MarketCap != 1_200_000_000_000 {
		t.Errorf("MarketCap: %v", btc.MarketCap)
	}
	if btc.Volume != 30_000_000_000 {
		t.Errorf("Volume: %v", btc.Volume)
	}
	if btc.SourceID != 99 {
		t.Errorf("SourceID: хотели 99, получили %d", btc.SourceID)
	}
	if btc.Change24H != 0.025 { // 2.5% -> 0.025
		t.Errorf("Change24H: хотели 0.025, получили %v", btc.Change24H)
	}
	wantTS := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	if !btc.TS.Equal(wantTS) {
		t.Errorf("TS: хотели %v, получили %v", wantTS, btc.TS)
	}

	// Отрицательное изменение тоже корректно переводится в доли.
	eth := points[1]
	if eth.Change24H != -0.0125 { // -1.25% -> -0.0125
		t.Errorf("ETH Change24H: хотели -0.0125, получили %v", eth.Change24H)
	}
}

// TestMarketMap_SkipsUnknownCoins — монеты, которых нет в справочнике,
// пропускаются (источник может вернуть лишнее).
func TestMarketMap_SkipsUnknownCoins(t *testing.T) {
	t.Parallel()
	assets := map[string]model.Asset{
		"bitcoin": {ID: 1, CoinID: "bitcoin"},
	}
	coins := []MarketCoin{
		{ID: "bitcoin", CurrentPrice: 60000, LastUpdated: "2026-08-12T10:00:00Z"},
		{ID: "dogecoin", CurrentPrice: 0.15, LastUpdated: "2026-08-12T10:00:00Z"}, // не отслеживаем
	}

	points := MarketMap(coins, assets, 1)
	if len(points) != 1 {
		t.Fatalf("хотели 1 точку (только bitcoin), получили %d", len(points))
	}
	if points[0].AssetID != 1 {
		t.Errorf("AssetID: хотели 1, получили %d", points[0].AssetID)
	}
}

// TestMarketMap_EmptyLastUpdatedFallsBackToNow — пустой LastUpdated не ломает
// маппинг: ts проставляется на now (не zero).
func TestMarketMap_EmptyLastUpdatedFallsBackToNow(t *testing.T) {
	t.Parallel()
	assets := map[string]model.Asset{
		"bitcoin": {ID: 1, CoinID: "bitcoin"},
	}
	coins := []MarketCoin{
		{ID: "bitcoin", CurrentPrice: 60000, LastUpdated: ""},
	}
	before := time.Now().Add(-1 * time.Second)
	points := MarketMap(coins, assets, 1)
	if len(points) != 1 {
		t.Fatalf("хотели 1 точку, получили %d", len(points))
	}
	if points[0].TS.Before(before) {
		t.Errorf("TS должен быть ~now, получили %v", points[0].TS)
	}
	if points[0].TS.IsZero() {
		t.Errorf("TS не должен быть zero")
	}
}

// TestMarketMap_EmptyInput — пустой вход даёт пустой (но не nil-zero для len)
// результат без паники.
func TestMarketMap_EmptyInput(t *testing.T) {
	t.Parallel()
	points := MarketMap(nil, map[string]model.Asset{}, 1)
	if len(points) != 0 {
		t.Fatalf("хотели 0 точек, получили %d", len(points))
	}
}
