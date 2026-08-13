// Package scoring — детерминированное ядро прогноза «вверх/вниз за 24ч».
//
// Чистая функция Forecast по факторам (rsi, momentum, volume, sentiment) считает
// прогноз: направление, уверенность, риск-ноту и текстовый аргумент. Никаких
// побочных эффектов и зависимостей от БД/сети — поведение полностью покрыто тестами.
//
// Формула (см. спеку T3/T4):
//
//	raw_score       = Σ(signal_i × adjusted_weight_i)
//	direction       = raw_score ≥ 0 ? up : down
//	confidence      = 0.5 + |raw_score|/2 − contradiction_penalty
//
// Базовые веса статические (адаптация — T5). Если sentiment-фактор не передан
// (нет новостей или нет OPENAI_API_KEY), прогноз считается по 3 факторам —
// веса перенормируются под их сумму (graceful degradation).
package scoring

import (
	"fmt"
	"math"
	"strings"
)

// Direction — направление прогноза.
type Direction string

const (
	DirectionUp   Direction = "up"
	DirectionDown Direction = "down"
)

// Horizon — горизонт прогноза в часах (пока фиксирован 24ч).
const Horizon = 24

// FactorName — идентификатор фактора.
type FactorName string

const (
	FactorRSI       FactorName = "rsi"
	FactorMomentum  FactorName = "momentum"
	FactorVolume    FactorName = "volume"
	FactorSentiment FactorName = "sentiment"
)

// DefaultBaseWeights — базовые веса факторов из спеки T3/T4.
// Сумма = 1.0: 0.25 + 0.35 + 0.15 + 0.25. При нормировке по присутствующим
// факторам веса масштабируются так, что их сумма равна 1.0. Если sentiment
// не передан — нормировка идёт по 3 факторам (сумма 0.75 → в 1.0).
var DefaultBaseWeights = map[FactorName]float64{
	FactorRSI:       0.25,
	FactorMomentum:  0.35,
	FactorVolume:    0.15,
	FactorSentiment: 0.25,
}

// Factor — входной фактор: имя, сигнал [-1..1] и человекочитаемый detail
// (используется при генерации argument_text и risk_note).
type Factor struct {
	Name   FactorName
	Signal float64 // [-1..1]: отрицательный → вниз, положительный → вверх
	Detail string  // описание значений, использованных для сигнала
}

// AdjustedFactor — фактор с пересчитанным весом после нормировки. Возвращается
// в ForecastResult.Factors для прозрачной декомпозиции вклада каждого фактора.
type AdjustedFactor struct {
	Name           FactorName
	Signal         float64
	BaseWeight     float64
	AdjustedWeight float64 // = base_weight / Σ(base_weight)
	Contribution   float64 // = signal × adjusted_weight
	Detail         string
}

// ForecastResult — итог прогноза: направление, уверенность, риск, текст и
// декомпозиция по факторам.
type ForecastResult struct {
	Direction    Direction
	Confidence   float64 // [0.5, 1.0]
	RiskNote     string
	ArgumentText string
	RawScore     float64
	Factors      []AdjustedFactor
}

// Forecast считает прогноз по факторам и базовым весам. weights может быть nil —
// тогда используются DefaultBaseWeights. Если weights не покрывает какой-то
// фактор, его базовый вес считается нулевым (фактор не влияет на прогноз).
//
// Детерминирована: одинаковые входы → одинаковый выход.
func Forecast(factors []Factor, weights map[FactorName]float64) ForecastResult {
	if weights == nil {
		weights = DefaultBaseWeights
	}

	// Сумма базовых весов по присутствующим факторам — для нормировки под 1.0.
	baseSum := 0.0
	for _, f := range factors {
		baseSum += weights[f.Name]
	}

	adjusted := make([]AdjustedFactor, 0, len(factors))
	rawScore := 0.0
	for _, f := range factors {
		base := weights[f.Name]
		adj := 0.0
		if baseSum > 0 {
			adj = base / baseSum
		}
		contrib := f.Signal * adj
		rawScore += contrib
		adjusted = append(adjusted, AdjustedFactor{
			Name:           f.Name,
			Signal:         f.Signal,
			BaseWeight:     base,
			AdjustedWeight: adj,
			Contribution:   contrib,
			Detail:         f.Detail,
		})
	}

	direction := DirectionUp
	if rawScore < 0 {
		direction = DirectionDown
	}

	confidence := computeConfidence(rawScore, adjusted, direction)

	return ForecastResult{
		Direction:    direction,
		Confidence:   confidence,
		RiskNote:     riskNote(confidence, adjusted),
		ArgumentText: argumentText(rawScore, direction, confidence, adjusted),
		RawScore:     rawScore,
		Factors:      adjusted,
	}
}

// computeConfidence: 0.5 + |raw_score|/2 со штрафом за противоречие факторов.
// Противоречие — если 2+ из присутствующих факторов смотрят в сторону,
// противоречащую итогу (signal направлен против direction). Каждый
// противоречащий фактор снижает confidence на 0.05.
func computeConfidence(rawScore float64, factors []AdjustedFactor, direction Direction) float64 {
	base := 0.5 + math.Abs(rawScore)/2

	contradicting := 0
	for _, f := range factors {
		if contradicts(f.Signal, direction) {
			contradicting++
		}
	}
	penalty := 0.0
	if contradicting >= 2 {
		penalty = float64(contradicting) * 0.05
	}

	conf := base - penalty
	// Зажимаем в [0.5, 1.0]: confidence не может быть ниже 0.5 (даже при сильном
	// противоречии — это «никакой» прогноз) и не выше 1.0.
	if conf < 0.5 {
		conf = 0.5
	}
	if conf > 1.0 {
		conf = 1.0
	}
	return conf
}

// contradicts — true, если сигнал направлен против итогового direction.
// Нулевой сигнал не противоречит ничему.
func contradicts(signal float64, direction Direction) bool {
	if signal == 0 {
		return false
	}
	if direction == DirectionUp {
		return signal < 0
	}
	return signal > 0
}

// confidenceLabel — слово для уровня уверенности в argument_text.
func confidenceLabel(conf float64) string {
	switch {
	case conf >= 0.8:
		return "высокая"
	case conf >= 0.65:
		return "умеренная"
	default:
		return "низкая"
	}
}

// riskNote — короткая заметка о риске на основе уверенности и противоречий.
func riskNote(conf float64, factors []AdjustedFactor) string {
	// Разнонаправленные сигналы среди ненулевых: если есть и позитивные, и
	// негативные — факторы противоречат друг другу.
	pos, neg := 0, 0
	for _, f := range factors {
		if f.Signal > 0 {
			pos++
		} else if f.Signal < 0 {
			neg++
		}
	}
	contradicting := 0
	if pos > 0 && neg > 0 {
		contradicting = min(pos, neg)
	}

	switch {
	case conf < 0.6:
		return "низкая уверенность — факторы противоречивы, прогноз ненадёжен"
	case contradicting >= 2:
		return "умеренный риск — часть факторов противоречит итогу"
	case conf >= 0.8:
		return "факторы согласованы, уверенность высокая"
	default:
		return "умеренная уверенность"
	}
}

// argumentText — детерминированная текстовая аргументация прогноза.
// Формат: «<фактор1>. <фактор2>. <фактор3>. Итог: <вверх/вниз>, <уровень> уверенность <conf>».
func argumentText(rawScore float64, direction Direction, conf float64, factors []AdjustedFactor) string {
	parts := make([]string, 0, len(factors))
	for _, f := range factors {
		if f.Detail != "" {
			parts = append(parts, f.Detail)
		}
	}
	dir := "вверх"
	if direction == DirectionDown {
		dir = "вниз"
	}
	summary := fmt.Sprintf("Итог: %s, %s уверенность %.2f", dir, confidenceLabel(conf), conf)
	if len(parts) == 0 {
		return summary
	}
	return strings.Join(parts, ". ") + ". " + summary
}
