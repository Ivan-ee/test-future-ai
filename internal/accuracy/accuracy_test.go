package accuracy

import (
	"math"
	"testing"

	"test-future/internal/model"
)

// tol — допуск сравнения float64 в тестах accuracy.
const tol = 1e-9

func approx(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s: хотели %v, получили %v", name, want, got)
	}
}

// --- Resolve ---

func TestResolve_HitWhenDirectionMatches(t *testing.T) {
	t.Parallel()
	// Прогноз «вверх», цена выросла на 5% → hit.
	result, changePct, actual := Resolve("up", 100, 105)
	if result != model.OutcomeHit {
		t.Errorf("хотели hit, получили %s", result)
	}
	approx(t, "change_pct", changePct, 5.0)
	if actual != "up" {
		t.Errorf("хотели actual=up, получили %s", actual)
	}
}

func TestResolve_MissWhenDirectionOpposite(t *testing.T) {
	t.Parallel()
	// Прогноз «вверх», цена упала на 2.1% → miss.
	result, changePct, actual := Resolve("up", 100, 97.9)
	if result != model.OutcomeMiss {
		t.Errorf("хотели miss, получили %s", result)
	}
	approx(t, "change_pct", changePct, -2.1)
	if actual != "down" {
		t.Errorf("хотели actual=down, получили %s", actual)
	}
}

func TestResolve_NeutralWhenChangeSmall(t *testing.T) {
	t.Parallel()
	// Изменение 0.3% — ниже порога 0.5% → neutral.
	result, _, _ := Resolve("up", 100, 100.3)
	if result != model.OutcomeNeutral {
		t.Errorf("хотели neutral при |change|<0.5%%, получили %s", result)
	}
}

func TestResolve_NeutralAtExactlyThreshold(t *testing.T) {
	t.Parallel()
	// |change| чуть выше порога 0.5% → hit (границаNeutralThreshold = 0.005,
	// сравнение строгое <). Берём 100.6 — гарантированно выше FP-погрешности.
	result, _, _ := Resolve("up", 100, 100.6)
	if result != model.OutcomeHit {
		t.Errorf("при |change|>0.5%% хотели hit, получили %s", result)
	}
	// |change| чуть ниже порога → neutral.
	result, _, _ = Resolve("up", 100, 100.3)
	if result != model.OutcomeNeutral {
		t.Errorf("при |change|<0.5%% хотели neutral, получили %s", result)
	}
}

func TestResolve_DownDirectionHit(t *testing.T) {
	t.Parallel()
	// Прогноз «вниз», цена упала на 3% → hit.
	result, _, actual := Resolve("down", 100, 97)
	if result != model.OutcomeHit {
		t.Errorf("хотели hit, получили %s", result)
	}
	if actual != "down" {
		t.Errorf("хотели actual=down, получили %s", actual)
	}
}

func TestResolve_ZeroForecastPrice(t *testing.T) {
	t.Parallel()
	// priceAtForecast=0 — ошибочные данные, возвращается neutral.
	result, changePct, _ := Resolve("up", 0, 100)
	if result != model.OutcomeNeutral {
		t.Errorf("при нулевой цене прогноза хотели neutral, получили %s", result)
	}
	approx(t, "change_pct при нулевой цене", changePct, 0)
}

// --- UpdateHitRateEMA ---

func TestUpdateHitRateEMA_HitsIncrease(t *testing.T) {
	t.Parallel()
	// Серия hits: каждый раз сигнал совпадает с фактом → EMA растёт.
	ema := 0.5 // стартовое значение
	for i := 0; i < 10; i++ {
		newEMA, hit := UpdateHitRateEMA(ema, 0.8, "up")
		if hit != 1 {
			t.Errorf("ожидали hit=1 при совпадении, получили %d", hit)
		}
		ema = newEMA
	}
	// После 10 hits EMA должно быть заметно выше 0.5.
	if ema <= 0.5 {
		t.Errorf("после серии hits EMA должно расти, получили %v", ema)
	}
	// При бесконечной серии hits EMA → 1.0.
	approx(t, "EMA после 10 hits", ema, 1.0-math.Pow(1-EMAAlpha, 10)*0.5)
}

func TestUpdateHitRateEMA_MissesDecrease(t *testing.T) {
	t.Parallel()
	// Серия misses: сигнал противоречит факту → EMA падает.
	ema := 0.5
	for i := 0; i < 10; i++ {
		newEMA, hit := UpdateHitRateEMA(ema, 0.8, "down") // сигнал +, факт down
		if hit != 0 {
			t.Errorf("ожидали hit=0 при противоречии, получили %d", hit)
		}
		ema = newEMA
	}
	if ema >= 0.5 {
		t.Errorf("после серии misses EMA должно падать, получили %v", ema)
	}
	// При бесконечной серии misses EMA → 0.0.
	approx(t, "EMA после 10 misses", ema, math.Pow(1-EMAAlpha, 10)*0.5)
}

func TestUpdateHitRateEMA_ZeroSignalCountsAsHit(t *testing.T) {
	t.Parallel()
	// Нулевой сигнал — нейтрально, считается совпадением.
	_, hit := UpdateHitRateEMA(0.5, 0, "up")
	if hit != 1 {
		t.Errorf("нулевой сигнал должен считаться совпадением (hit=1), получили %d", hit)
	}
}

func TestUpdateHitRateEMA_AlphaCorrect(t *testing.T) {
	t.Parallel()
	// EMA_new = 0.8×0.5 + 0.2×1 = 0.4 + 0.2 = 0.6 при hit.
	ema, _ := UpdateHitRateEMA(0.5, 1.0, "up")
	approx(t, "EMA при одном hit", ema, 0.6)
	// EMA_new = 0.8×0.5 + 0.2×0 = 0.4 при miss.
	ema, _ = UpdateHitRateEMA(0.5, 1.0, "down")
	approx(t, "EMA при одном miss", ema, 0.4)
}

// --- AdjustedWeight ---

func TestAdjustedWeight_NeutralEMAMultiplierOne(t *testing.T) {
	t.Parallel()
	// EMA=0.5 → множитель 1.0 → вес не меняется.
	approx(t, "adjusted при EMA=0.5", AdjustedWeight(0.35, 0.5), 0.35)
}

func TestAdjustedWeight_HighEMAIncreasesWeight(t *testing.T) {
	t.Parallel()
	// EMA=1.0 → множитель clamp(1.0/0.5, 0.5, 1.5) = clamp(2.0, ...) = 1.5.
	approx(t, "adjusted при EMA=1.0", AdjustedWeight(0.35, 1.0), 0.35*1.5)
}

func TestAdjustedWeight_LowEMADecreasesWeight(t *testing.T) {
	t.Parallel()
	// EMA=0.0 → множитель clamp(0/0.5, 0.5, 1.5) = clamp(0, ...) = 0.5.
	approx(t, "adjusted при EMA=0.0", AdjustedWeight(0.35, 0.0), 0.35*0.5)
}

func TestAdjustedWeight_ClampsAtBounds(t *testing.T) {
	t.Parallel()
	// EMA=0.75 → множитель 1.5 (clamp). EMA=0.25 → множитель 0.5 (clamp).
	approx(t, "adjusted при EMA=0.75 (верхний clamp)", AdjustedWeight(0.35, 0.75), 0.35*1.5)
	approx(t, "adjusted при EMA=0.25 (нижний clamp)", AdjustedWeight(0.35, 0.25), 0.35*0.5)
}

// --- AttributeMiss ---

func TestAttributeMiss_ReturnsMaxContradictingFactor(t *testing.T) {
	t.Parallel()
	// Промах: цена упала, но прогноз был вверх.
	// momentum дал максимальный вклад вверх (+0.35) → он главный виновник.
	factors := []model.ForecastFactor{
		{Name: "rsi", Signal: 0.3, Contribution: 0.075},
		{Name: "momentum", Signal: 1.0, Contribution: 0.35},
		{Name: "volume", Signal: -0.2, Contribution: -0.03},
	}
	culprit, explanation := AttributeMiss(factors, "down")
	if culprit != "momentum" {
		t.Errorf("хотели culprit=momentum (макс противоречащий), получили %s", culprit)
	}
	if explanation == "" {
		t.Error("explanation не должен быть пустым")
	}
}

func TestAttributeMiss_PrefersContradictingOverAgreeing(t *testing.T) {
	t.Parallel()
	// volume имеет макс |contribution| среди всех, но он согласуется с фактом (down).
	// momentum противоречит (вверх) — он должен стать culprit, хотя его вклад меньше.
	factors := []model.ForecastFactor{
		{Name: "rsi", Signal: 0.3, Contribution: 0.075},
		{Name: "momentum", Signal: 0.8, Contribution: 0.28},
		{Name: "volume", Signal: -1.0, Contribution: -0.15},
	}
	culprit, _ := AttributeMiss(factors, "down")
	if culprit != "momentum" {
		t.Errorf("хотели culprit=momentum (противоречит факту), получили %s", culprit)
	}
}

func TestAttributeMiss_NoContradicting_FallbackToMax(t *testing.T) {
	t.Parallel()
	// Все факторы согласуются с фактом (маловероятно при miss, но проверяем fallback).
	// Берём макс |contribution| — momentum.
	factors := []model.ForecastFactor{
		{Name: "rsi", Signal: -0.3, Contribution: -0.075},
		{Name: "momentum", Signal: -1.0, Contribution: -0.35},
	}
	culprit, _ := AttributeMiss(factors, "down")
	if culprit != "momentum" {
		t.Errorf("fallback: хотели momentum (макс |contribution|), получили %s", culprit)
	}
}

func TestAttributeMiss_EmptyFactors(t *testing.T) {
	t.Parallel()
	culprit, explanation := AttributeMiss(nil, "down")
	if culprit != "" || explanation != "" {
		t.Errorf("при пустых факторах хотели пустые строки, получили %q / %q", culprit, explanation)
	}
}

// --- AttributeHit ---

func TestAttributeHit_ReturnsLeadingFactor(t *testing.T) {
	t.Parallel()
	// Попадание: цена выросла. momentum — макс согласующийся вклад.
	factors := []model.ForecastFactor{
		{Name: "rsi", Signal: 0.5, Contribution: 0.125},
		{Name: "momentum", Signal: 0.8, Contribution: 0.28},
		{Name: "volume", Signal: -0.2, Contribution: -0.03},
	}
	leader, explanation := AttributeHit(factors, "up")
	if leader != "momentum" {
		t.Errorf("хотели leader=momentum (макс согласующийся), получили %s", leader)
	}
	if explanation == "" {
		t.Error("explanation не должен быть пустым")
	}
}

// --- Интеграционный сценарий: серия hits/misses двигает EMA и вес ---

func TestSeries_HitsRaiseEMAAndWeight(t *testing.T) {
	t.Parallel()
	// Начинаем с EMA=0.5, base_weight=0.35. После серии hits EMA растёт → вес растёт.
	ema := 0.5
	const baseWeight = 0.35
	weightBefore := AdjustedWeight(baseWeight, ema)

	for i := 0; i < 20; i++ {
		ema, _ = UpdateHitRateEMA(ema, 0.7, "up")
	}
	weightAfter := AdjustedWeight(baseWeight, ema)

	if weightAfter <= weightBefore {
		t.Errorf("после серии hits вес должен расти: до %v, после %v (EMA=%v)",
			weightBefore, weightAfter, ema)
	}
}

func TestSeries_MissesLowerEMAAndWeight(t *testing.T) {
	t.Parallel()
	// Начинаем с EMA=0.5, base_weight=0.35. После серии misses EMA падает → вес падает.
	ema := 0.5
	const baseWeight = 0.35
	weightBefore := AdjustedWeight(baseWeight, ema)

	for i := 0; i < 20; i++ {
		ema, _ = UpdateHitRateEMA(ema, 0.7, "down") // сигнал +, факт down — miss
	}
	weightAfter := AdjustedWeight(baseWeight, ema)

	if weightAfter >= weightBefore {
		t.Errorf("после серии misses вес должен падать: до %v, после %v (EMA=%v)",
			weightBefore, weightAfter, ema)
	}
}
