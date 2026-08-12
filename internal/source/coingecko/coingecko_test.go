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

// --- ChartMap ---

// TestChartMap_HappyPath — корректный маппинг [ts_ms, value] в точки цен:
// timestamp из миллисекунд, цена и объём сопоставлены по индексу.
func TestChartMap_HappyPath(t *testing.T) {
	t.Parallel()
	chart := MarketChart{
		Prices: [][2]float64{
			{1723334400000, 60000}, // 2024-08-11T00:00:00Z
			{1723420800000, 61000}, // 2024-08-12T00:00:00Z
		},
		TotalVolumes: [][2]float64{
			{1723334400000, 30_000_000_000},
			{1723420800000, 32_000_000_000},
		},
	}

	points := ChartMap(chart, 1, 99)
	if len(points) != 2 {
		t.Fatalf("хотели 2 точки, получили %d", len(points))
	}

	wantTS := time.Date(2024, 8, 11, 0, 0, 0, 0, time.UTC)
	if !points[0].TS.Equal(wantTS) {
		t.Errorf("TS[0]: хотели %v, получили %v", wantTS, points[0].TS)
	}
	if points[0].PriceUSD != 60000 {
		t.Errorf("Price[0]: хотели 60000, получили %v", points[0].PriceUSD)
	}
	if points[0].Volume != 30_000_000_000 {
		t.Errorf("Volume[0]: хотели 30e9, получили %v", points[0].Volume)
	}
	if points[1].PriceUSD != 61000 {
		t.Errorf("Price[1]: хотели 61000, получили %v", points[1].PriceUSD)
	}
	if points[1].AssetID != 1 || points[1].SourceID != 99 {
		t.Errorf("ключи: AssetID=%d SourceID=%d", points[1].AssetID, points[1].SourceID)
	}
}

// TestChartMap_VolumesShorterThanPrices — если ряд объёмов короче цен,
// недостающие объёмы заполняются нулём (точка остаётся).
func TestChartMap_VolumesShorterThanPrices(t *testing.T) {
	t.Parallel()
	chart := MarketChart{
		Prices: [][2]float64{
			{1723334400000, 60000},
			{1723420800000, 61000},
			{1723507200000, 62000},
		},
		TotalVolumes: [][2]float64{
			{1723334400000, 30e9}, // только одна
		},
	}

	points := ChartMap(chart, 1, 1)
	if len(points) != 3 {
		t.Fatalf("хотели 3 точки, получили %d", len(points))
	}
	if points[0].Volume != 30e9 {
		t.Errorf("Volume[0]: хотели 30e9, получили %v", points[0].Volume)
	}
	if points[1].Volume != 0 {
		t.Errorf("Volume[1]: хотели 0 (нет в ряде), получили %v", points[1].Volume)
	}
	if points[2].Volume != 0 {
		t.Errorf("Volume[2]: хотели 0 (нет в ряде), получили %v", points[2].Volume)
	}
	if points[2].PriceUSD != 62000 {
		t.Errorf("Price[2]: хотели 62000, получили %v", points[2].PriceUSD)
	}
}

// TestChartMap_EmptyChart — пустой ответ не паникует, отдаёт пустой срез.
func TestChartMap_EmptyChart(t *testing.T) {
	t.Parallel()
	points := ChartMap(MarketChart{}, 1, 1)
	if len(points) != 0 {
		t.Fatalf("хотели 0 точек, получили %d", len(points))
	}
}
