package scoring

import (
	"math"
	"testing"
)

// tol — допуск сравнения float64 в тестах scoring.
const tol = 1e-9

func approx(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s: хотели %v, получили %v", name, want, got)
	}
}

// threeBullish — три фактора, все смотрят вверх (сигналы +1).
func threeBullish() []Factor {
	return []Factor{
		{Name: FactorRSI, Signal: 1, Detail: "RSI нейтрален"},
		{Name: FactorMomentum, Signal: 1, Detail: "моментум вверх"},
		{Name: FactorVolume, Signal: 1, Detail: "объём выше среднего"},
	}
}

// threeBearish — три фактора, все смотрят вниз (сигналы -1).
func threeBearish() []Factor {
	return []Factor{
		{Name: FactorRSI, Signal: -1},
		{Name: FactorMomentum, Signal: -1},
		{Name: FactorVolume, Signal: -1},
	}
}

// --- direction == "up" iff raw_score >= 0 ---

func TestForecast_DirectionUpWhenRawScorePositive(t *testing.T) {
	t.Parallel()
	r := Forecast(threeBullish(), nil)
	if r.Direction != DirectionUp {
		t.Errorf("хотели up, получили %s (raw=%v)", r.Direction, r.RawScore)
	}
}

func TestForecast_DirectionUpWhenRawScoreZero(t *testing.T) {
	t.Parallel()
	// Все сигналы 0 → raw_score = 0 → direction = up (по правилу >= 0).
	factors := []Factor{
		{Name: FactorRSI, Signal: 0},
		{Name: FactorMomentum, Signal: 0},
		{Name: FactorVolume, Signal: 0},
	}
	r := Forecast(factors, nil)
	if r.Direction != DirectionUp {
		t.Errorf("raw_score=0: хотели up, получили %s", r.Direction)
	}
}

func TestForecast_DirectionDownWhenRawScoreNegative(t *testing.T) {
	t.Parallel()
	r := Forecast(threeBearish(), nil)
	if r.Direction != DirectionDown {
		t.Errorf("хотели down, получили %s (raw=%v)", r.Direction, r.RawScore)
	}
}

// --- confidence ∈ [0.5, 1.0] ---

func TestForecast_ConfidenceInRange_Bullish(t *testing.T) {
	t.Parallel()
	r := Forecast(threeBullish(), nil)
	if r.Confidence < 0.5 || r.Confidence > 1.0 {
		t.Errorf("confidence вне [0.5,1.0]: %v", r.Confidence)
	}
}

func TestForecast_ConfidenceInRange_Bearish(t *testing.T) {
	t.Parallel()
	r := Forecast(threeBearish(), nil)
	if r.Confidence < 0.5 || r.Confidence > 1.0 {
		t.Errorf("confidence вне [0.5,1.0]: %v", r.Confidence)
	}
}

func TestForecast_ConfidenceMinHalfAtZeroRaw(t *testing.T) {
	t.Parallel()
	factors := []Factor{
		{Name: FactorRSI, Signal: 0},
		{Name: FactorMomentum, Signal: 0},
		{Name: FactorVolume, Signal: 0},
	}
	r := Forecast(factors, nil)
	approx(t, "confidence при raw=0", r.Confidence, 0.5)
}

func TestForecast_ConfidenceMaxOneAtFullAgreement(t *testing.T) {
	t.Parallel()
	// Все сигналы +1, веса нормированы → raw_score = 1.0 → confidence = 1.0.
	r := Forecast(threeBullish(), nil)
	approx(t, "confidence при полном согласии", r.Confidence, 1.0)
}

// --- Σ base_weight == 1.0 после нормировки (для 3 факторов) ---

func TestForecast_AdjustedWeightsSumToOne(t *testing.T) {
	t.Parallel()
	r := Forecast(threeBullish(), nil)
	sum := 0.0
	for _, f := range r.Factors {
		sum += f.AdjustedWeight
	}
	approx(t, "Σ adjusted_weight", sum, 1.0)
}

// --- raw_score == Σ(signal × adjusted_weight) ---

func TestForecast_RawScoreEqualsWeightedSignalSum(t *testing.T) {
	t.Parallel()
	factors := []Factor{
		{Name: FactorRSI, Signal: 0.6},
		{Name: FactorMomentum, Signal: -0.3},
		{Name: FactorVolume, Signal: 0.8},
	}
	r := Forecast(factors, nil)
	want := 0.0
	for _, f := range r.Factors {
		want += f.Signal * f.AdjustedWeight
	}
	approx(t, "raw_score", r.RawScore, want)
}

// --- при противоречии факторов confidence ниже, чем при согласии ---

func TestForecast_ContradictionLowersConfidence(t *testing.T) {
	t.Parallel()
	// Согласие: все три смотрят вверх с умеренной силой.
	agree := []Factor{
		{Name: FactorRSI, Signal: 0.8},
		{Name: FactorMomentum, Signal: 0.8},
		{Name: FactorVolume, Signal: 0.8},
	}
	// Противоречие: итог вверх (momentum+volume сильнее), но RSI против.
	contradict := []Factor{
		{Name: FactorRSI, Signal: -0.9},
		{Name: FactorMomentum, Signal: 0.9},
		{Name: FactorVolume, Signal: 0.9},
	}

	rAgree := Forecast(agree, nil)
	rContra := Forecast(contradict, nil)

	// Оба прогноза — вверх (momentum доминирует), но у противоречивого
	// confidence должно быть заметно ниже из-за штрафа за противоречие.
	if rContra.Direction != DirectionUp {
		t.Fatalf("противоречивый прогноз: хотели up, получили %s", rContra.Direction)
	}
	if rContra.Confidence >= rAgree.Confidence {
		t.Errorf("при противоречии confidence должен быть ниже: согласие=%v, противоречие=%v",
			rAgree.Confidence, rContra.Confidence)
	}
}

// --- нормировка весов под 3 фактора: RSI 0.25, Momentum 0.35, Volume 0.15 ---

func TestForecast_AdjustedWeightsProportionalToBase(t *testing.T) {
	t.Parallel()
	// Базовая сумма 0.75 → нормированные: 1/3, 7/15, 1/5.
	r := Forecast(threeBullish(), nil)
	want := map[FactorName]float64{
		FactorRSI:      0.25 / 0.75,
		FactorMomentum: 0.35 / 0.75,
		FactorVolume:   0.15 / 0.75,
	}
	for _, f := range r.Factors {
		approx(t, "adjusted_weight "+string(f.Name), f.AdjustedWeight, want[f.Name])
	}
}

// --- Contribution = signal × adjusted_weight ---

func TestForecast_ContributionIsSignalTimesWeight(t *testing.T) {
	t.Parallel()
	factors := []Factor{
		{Name: FactorRSI, Signal: -0.5},
		{Name: FactorMomentum, Signal: 0.4},
		{Name: FactorVolume, Signal: 0.9},
	}
	r := Forecast(factors, nil)
	for _, f := range r.Factors {
		approx(t, "contribution "+string(f.Name), f.Contribution, f.Signal*f.AdjustedWeight)
	}
}

// --- детерминированность ---

func TestForecast_Deterministic(t *testing.T) {
	t.Parallel()
	r1 := Forecast(threeBullish(), nil)
	r2 := Forecast(threeBullish(), nil)
	if r1.RawScore != r2.RawScore || r1.Confidence != r2.Confidence ||
		r1.Direction != r2.Direction {
		t.Error("Forecast не детерминирована при одинаковых входах")
	}
}

// --- argument_text и risk_note не пустые ---

func TestForecast_TextsNotEmpty(t *testing.T) {
	t.Parallel()
	r := Forecast(threeBullish(), nil)
	if r.ArgumentText == "" {
		t.Error("argument_text пустой")
	}
	if r.RiskNote == "" {
		t.Error("risk_note пустой")
	}
}

// --- сигналы за пределами [-1..1] тоже корректно обрабатываются (clamping не требуется,
// но формула должна работать численно) ---

func TestForecast_PartialSignals(t *testing.T) {
	t.Parallel()
	factors := []Factor{
		{Name: FactorRSI, Signal: 0.72},      // перекупленность → слабый вниз, но здесь тест числа
		{Name: FactorMomentum, Signal: -0.3}, // падение
		{Name: FactorVolume, Signal: 0.1},    // норма
	}
	r := Forecast(factors, nil)
	// raw_score должен быть умеренно положительным из-за RSI.
	if r.Direction != DirectionUp && r.Direction != DirectionDown {
		t.Errorf("неожиданное направление: %s", r.Direction)
	}
	// confidence всегда в диапазоне.
	if r.Confidence < 0.5 || r.Confidence > 1.0 {
		t.Errorf("confidence вне диапазона: %v", r.Confidence)
	}
}

// --- T4: sentiment-фактор ---

// fourBullish — четыре фактора, все смотрят вверх (сигналы +1), включая sentiment.
func fourBullish() []Factor {
	return []Factor{
		{Name: FactorRSI, Signal: 1, Detail: "RSI нейтрален"},
		{Name: FactorMomentum, Signal: 1, Detail: "моментум вверх"},
		{Name: FactorVolume, Signal: 1, Detail: "объём выше среднего"},
		{Name: FactorSentiment, Signal: 1, Detail: "сентимент позитивный"},
	}
}

// fourBearish — четыре фактора, все смотрят вниз (сигналы -1), включая sentiment.
func fourBearish() []Factor {
	return []Factor{
		{Name: FactorRSI, Signal: -1},
		{Name: FactorMomentum, Signal: -1},
		{Name: FactorVolume, Signal: -1},
		{Name: FactorSentiment, Signal: -1},
	}
}

// TestForecast_FourFactors_WeightsSumToOne — при 4 факторах веса суммируются в 1.0.
func TestForecast_FourFactors_WeightsSumToOne(t *testing.T) {
	t.Parallel()
	r := Forecast(fourBullish(), nil)
	sum := 0.0
	for _, f := range r.Factors {
		sum += f.AdjustedWeight
	}
	approx(t, "Σ adjusted_weight (4 фактора)", sum, 1.0)
}

// TestForecast_FourFactors_AdjustedWeightsProportional — веса 4 факторов
// пропорциональны базовым (0.25+0.35+0.15+0.25=1.0, т.е. нормировка не меняет их).
func TestForecast_FourFactors_AdjustedWeightsProportional(t *testing.T) {
	t.Parallel()
	r := Forecast(fourBullish(), nil)
	// Базовая сумма 1.0 → нормированные равны базовым.
	want := map[FactorName]float64{
		FactorRSI:       0.25,
		FactorMomentum:  0.35,
		FactorVolume:    0.15,
		FactorSentiment: 0.25,
	}
	for _, f := range r.Factors {
		approx(t, "adjusted_weight "+string(f.Name), f.AdjustedWeight, want[f.Name])
	}
}

// TestForecast_FourFactors_FullAgreementConfidenceOne — все 4 фактора +1 →
// raw_score=1.0, confidence=1.0.
func TestForecast_FourFactors_FullAgreementConfidenceOne(t *testing.T) {
	t.Parallel()
	r := Forecast(fourBullish(), nil)
	approx(t, "confidence (4 фактора, согласие)", r.Confidence, 1.0)
}

// TestForecast_GracefulDegradation_ThreeFactors — без sentiment прогноз считается
// по 3 факторам; веса перенормируются под их сумму (0.75 → 1.0).
func TestForecast_GracefulDegradation_ThreeFactors(t *testing.T) {
	t.Parallel()
	r := Forecast(threeBullish(), nil)
	if len(r.Factors) != 3 {
		t.Fatalf("хотели 3 фактора (без sentiment), получили %d", len(r.Factors))
	}
	sum := 0.0
	for _, f := range r.Factors {
		sum += f.AdjustedWeight
	}
	approx(t, "Σ adjusted_weight (3 фактора)", sum, 1.0)
	// direction и confidence в норме.
	if r.Direction != DirectionUp {
		t.Errorf("3 bullish фактора: хотели up, получили %s", r.Direction)
	}
}

// TestForecast_SentimentFlipsDirection — sentiment может перевесить направление:
// 3 технических фактора умеренно вверх, но сильный негативный сентимент — итог вниз.
func TestForecast_SentimentFlipsDirection(t *testing.T) {
	t.Parallel()
	factors := []Factor{
		{Name: FactorRSI, Signal: 0.2},
		{Name: FactorMomentum, Signal: 0.2},
		{Name: FactorVolume, Signal: 0.2},
		{Name: FactorSentiment, Signal: -1}, // сильный негатив
	}
	r := Forecast(factors, nil)
	// Сентимент (вес 0.25, сигнал -1) перевешивает три слабых позитивных фактора.
	// Вклад sentiment: -1 × 0.25 = -0.25. Вклад остальных: (0.2×0.25 + 0.2×0.35 + 0.2×0.15) = 0.15.
	// raw_score = 0.15 - 0.25 = -0.1 → down.
	if r.Direction != DirectionDown {
		t.Errorf("сильный негативный сентимент: хотели down, получили %s (raw=%v)",
			r.Direction, r.RawScore)
	}
}
