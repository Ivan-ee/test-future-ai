// Package accuracy — ядро сверки прогнозов с фактом, атрибуции ошибок и
// адаптации весов факторов.
//
// Чистые функции без зависимостей от БД/сети — поведение полностью покрыто тестами,
// аналогично пакету scoring. Слой worker вызывает эти функции в resolve-цикле (раз
// в час для прогнозов старше 24ч) и при расчёте новых прогнозов (адаптация весов).
//
// Логика (см. спеку T5):
//
//	Resolve: price_change_pct = (resolution/forecast − 1) × 100
//	  result = hit  — направление прогноза совпало с фактическим и |change| ≥ 0.5%
//	  result = miss — направление не совпало и |change| ≥ 0.5%
//	  result = neutral — |change| < 0.5% (слишком маленькое движение)
//
//	Attribute: при miss culprit = фактор с макс |contribution|, чей знак противоречит
//	  actual_direction; при hit — ведущий фактор (макс согласующийся вклад).
//
//	UpdateHitRateEMA: EMA = 0.8×EMA + 0.2×(1 если знак signal совпал с фактом, иначе 0)
//
//	AdjustedWeight: base_weight × clamp(hit_rate_ema / 0.5, 0.5, 1.5)
//	  — фактор, что часто ошибается, получает понижение.
package accuracy

import (
	"fmt"
	"math"

	"test-future/internal/model"
)

// Пороговые константы из спеки T5.
const (
	// NeutralThreshold — изменение цены ниже этого порога (в долях) считается
	// нейтральным: 0.005 = 0.5%.
	NeutralThreshold = 0.005

	// EMAAlpha — коэффициент обновления hit_rate_ema (α=0.2 по спеке).
	EMAAlpha = 0.2
)

// Resolve сверяет направление прогноза с фактическим изменением цены.
// Возвращает result, price_change_pct (в процентах) и actual_direction ("up"|"down").
//
// Если priceAtForecast ≤ 0 — это ошибочные данные, возвращается neutral и 0%.
func Resolve(direction string, priceAtForecast, priceAtResolution float64) (model.OutcomeResult, float64, string) {
	if priceAtForecast <= 0 {
		return model.OutcomeNeutral, 0, "up"
	}

	changeFrac := priceAtResolution/priceAtForecast - 1
	changePct := changeFrac * 100

	actualDirection := "up"
	if changeFrac < 0 {
		actualDirection = "down"
	}

	// Слишком маленькое движение — neutral, независимо от направления.
	if math.Abs(changeFrac) < NeutralThreshold {
		return model.OutcomeNeutral, changePct, actualDirection
	}

	if actualDirection == direction {
		return model.OutcomeHit, changePct, actualDirection
	}
	return model.OutcomeMiss, changePct, actualDirection
}

// AttributeMiss находит «виновный» фактор в промахе: фактор с максимальным
// |contribution|, чей знак противоречит actual_direction (сигнал направлен в
// сторону, противоположную факту). Генерирует человекочитаемое explanation.
//
// Если ни один фактор не противоречит факту (маловероятно при miss, но возможно
// при слабых сигналах) — возвращается фактор с макс |contribution| среди всех.
func AttributeMiss(factors []model.ForecastFactor, actualDirection string) (string, string) {
	if len(factors) == 0 {
		return "", ""
	}

	var culprit *model.ForecastFactor
	maxAbs := math.Inf(-1)

	// Среди противоречащих факторов — максимальный по модулю вклад.
	for i := range factors {
		f := &factors[i]
		if contradictsFactor(f.Signal, actualDirection) {
			abs := math.Abs(f.Contribution)
			if abs > maxAbs {
				maxAbs = abs
				culprit = f
			}
		}
	}

	// Если противоречащих нет — берём максимальный по модулю среди всех.
	if culprit == nil {
		for i := range factors {
			f := &factors[i]
			abs := math.Abs(f.Contribution)
			if abs > maxAbs {
				maxAbs = abs
				culprit = f
			}
		}
	}

	return culprit.Name, missExplanation(culprit, actualDirection)
}

// missExplanation генерирует человекочитаемое объяснение промаха для фактора.
// Формат: «<factor> давал <+/−contribution> (<detail без знака сигнала>),
// но цена <выросла/упала> на <change>% — <вывод>».
func missExplanation(f *model.ForecastFactor, actualDirection string) string {
	signalDir := "вверх"
	if f.Signal < 0 {
		signalDir = "вниз"
	}
	actualDir := "выросла"
	if actualDirection == "down" {
		actualDir = "упала"
	}
	sign := "+"
	if f.Contribution < 0 {
		sign = ""
	}
	return fmt.Sprintf(
		"%s давал %s%.4f (сигнал %s, %.2f), но цена %s — фактор увёл прогноз в сторону",
		f.Name, sign, f.Contribution, signalDir, f.Signal, actualDir,
	)
}

// AttributeHit находит ведущий фактор при попадании: фактор с максимальным
// вкладом, чей знак совпадает с actual_direction (согласующийся вклад).
// Если согласующихся нет — фактор с макс |contribution|.
func AttributeHit(factors []model.ForecastFactor, actualDirection string) (string, string) {
	if len(factors) == 0 {
		return "", ""
	}

	var leader *model.ForecastFactor
	maxContrib := math.Inf(-1)

	for i := range factors {
		f := &factors[i]
		if agreesFactor(f.Signal, actualDirection) {
			if f.Contribution > maxContrib {
				maxContrib = f.Contribution
				leader = f
			}
		}
	}

	if leader == nil {
		// Нет согласующихся — берём макс |contribution| (фактор всё равно был ведущим
		// в расчёте, даже если его знак не совпал с фактом — случай neutral).
		maxAbs := math.Inf(-1)
		for i := range factors {
			f := &factors[i]
			abs := math.Abs(f.Contribution)
			if abs > maxAbs {
				maxAbs = abs
				leader = f
			}
		}
	}

	return leader.Name, hitExplanation(leader, actualDirection)
}

// hitExplanation — короткое объяснение ведущего фактора при попадании.
func hitExplanation(f *model.ForecastFactor, actualDirection string) string {
	dir := "вверх"
	if actualDirection == "down" {
		dir = "вниз"
	}
	return fmt.Sprintf(
		"%s верно указал %s (вклад %.4f, сигнал %.2f) — главный драйвер точного прогноза",
		f.Name, dir, f.Contribution, f.Signal,
	)
}

// UpdateHitRateEMA обновляет скользящее среднее доли совпадений знака фактора с
// фактом. Возвращает новое EMA и флаг попадания (1 если знак signal совпал с
// actual_direction, иначе 0). Нулевой сигнал считается совпадением (нейтрально).
//
//	EMA_new = (1−α)×EMA_old + α×hit
func UpdateHitRateEMA(currentEMA float64, factorSignal float64, actualDirection string) (float64, int) {
	hit := 0
	if factorSignal == 0 || signalMatchesDirection(factorSignal, actualDirection) {
		hit = 1
	}
	newEMA := (1-EMAAlpha)*currentEMA + EMAAlpha*float64(hit)
	return newEMA, hit
}

// AdjustedWeight считает адаптированный вес фактора на основе hit_rate_ema:
//
//	adjusted = base_weight × clamp(hit_rate_ema / 0.5, 0.5, 1.5)
//
// При EMA=0.5 (нейтрально) множитель = 1.0; при EMA→1.0 → множитель 1.5 (часто
// угадывает — повышение); при EMA→0.0 → множитель 0.5 (часто ошибается — понижение).
func AdjustedWeight(baseWeight, hitRateEMA float64) float64 {
	multiplier := hitRateEMA / 0.5
	multiplier = clamp(multiplier, 0.5, 1.5)
	return baseWeight * multiplier
}

// contradictsFactor — true, если сигнал фактора направлен против actual_direction.
// Нулевой сигнал не противоречит.
func contradictsFactor(signal float64, actualDirection string) bool {
	if signal == 0 {
		return false
	}
	if actualDirection == "up" {
		return signal < 0
	}
	return signal > 0
}

// agreesFactor — true, если сигнал фактора совпадает с actual_direction.
func agreesFactor(signal float64, actualDirection string) bool {
	if signal == 0 {
		return false
	}
	return !contradictsFactor(signal, actualDirection)
}

// signalMatchesDirection — true, если знак signal ≥ 0 для "up" или ≤ 0 для "down".
func signalMatchesDirection(signal float64, direction string) bool {
	if direction == "up" {
		return signal >= 0
	}
	return signal <= 0
}

// clamp зажимает значение в [lo, hi].
func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
