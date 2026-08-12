// Package indicator — технические индикаторы на чистом Go.
//
// Чистые функции без зависимостей от БД и сети: RSI (формула Уайлдера),
// ROC, SMA и VolumeSignal. Принимают ряды цен/объёмов, возвращают значения
// и ошибки при недостатке данных. Поведение покрыто юнит-тестами.
//
// Интерпретации (InterpretRSI и др.) — человекочитаемые подписи к значениям,
// используются серверным слоем для DTO.
package indicator

import (
	"errors"
	"fmt"
)

// Ошибки insufficientData возвращают функции при нехватке точек ряда.
var (
	ErrInsufficientData = errors.New("недостаточно данных для расчёта индикатора")
	ErrInvalidPeriod    = errors.New("период должен быть положительным")
)

// DefaultVolumeTolerance — зона нормы вокруг 1.0 для VolumeSignal.
// Единый источник правды: используется и при расчёте (worker), и при
// интерпретации (server), чтобы значения и подписи не разъехались.
const DefaultVolumeTolerance = 0.2

// SMA — простая скользящая средняя последних n элементов values.
// n должно быть положительным и не превышать длину ряда.
func SMA(values []float64, n int) (float64, error) {
	if n <= 0 {
		return 0, fmt.Errorf("%w: n=%d", ErrInvalidPeriod, n)
	}
	if len(values) < n {
		return 0, fmt.Errorf("%w: нужно %d точек, есть %d", ErrInsufficientData, n, len(values))
	}
	var sum float64
	start := len(values) - n
	for _, v := range values[start:] {
		sum += v
	}
	return sum / float64(n), nil
}

// ROC — скорость изменения цены за n периодов, в процентах.
// Формула: ((last - closes[len-1-n]) / closes[len-1-n]) * 100.
func ROC(closes []float64, n int) (float64, error) {
	if n <= 0 {
		return 0, fmt.Errorf("%w: n=%d", ErrInvalidPeriod, n)
	}
	if len(closes) < n+1 {
		return 0, fmt.Errorf("%w: для ROC нужно %d точек, есть %d", ErrInsufficientData, n+1, len(closes))
	}
	prev := closes[len(closes)-1-n]
	last := closes[len(closes)-1]
	if prev == 0 {
		return 0, fmt.Errorf("%w: предыдущая цена равна 0", ErrInsufficientData)
	}
	return (last - prev) / prev * 100, nil
}

// RSI — индекс относительной силы по формуле Уайлдера.
// Первая средняя прибыль/убыток — простая (за n периодов), далее —
// экспоненциальное сглаживание: avg = (prev*(n-1) + cur) / n.
// RS = avgGain / avgLoss; RSI = 100 - 100/(1+RS).
// Если нет ни прибыли, ни убытков (ряд постоянный) — RSI = 50.
func RSI(closes []float64, n int) (float64, error) {
	if n <= 0 {
		return 0, fmt.Errorf("%w: n=%d", ErrInvalidPeriod, n)
	}
	if len(closes) < n+1 {
		return 0, fmt.Errorf("%w: для RSI нужно %d точек, есть %d", ErrInsufficientData, n+1, len(closes))
	}

	// Изменения между соседними ценами.
	changes := make([]float64, len(closes)-1)
	for i := 1; i < len(closes); i++ {
		changes[i-1] = closes[i] - closes[i-1]
	}

	// Первый расчёт средних — простая средняя за n периодов.
	var gainSum, lossSum float64
	for _, ch := range changes[:n] {
		if ch > 0 {
			gainSum += ch
		} else {
			lossSum -= ch // ch отрицательный, берём модуль
		}
	}
	avgGain := gainSum / float64(n)
	avgLoss := lossSum / float64(n)

	// Сглаживание Уайлдера для остальных периодов.
	for _, ch := range changes[n:] {
		var gain, loss float64
		if ch > 0 {
			gain = ch
		} else {
			loss = -ch
		}
		avgGain = (avgGain*float64(n-1) + gain) / float64(n)
		avgLoss = (avgLoss*float64(n-1) + loss) / float64(n)
	}

	// Нет ни прибыли, ни убытков — нейтральный рынок.
	if avgGain == 0 && avgLoss == 0 {
		return 50, nil
	}
	if avgLoss == 0 {
		return 100, nil // только рост
	}
	if avgGain == 0 {
		return 0, nil // только падение
	}
	rs := avgGain / avgLoss
	return 100 - 100/(1+rs), nil
}

// VolumeSignal — отношение последнего объёма к средней за n периодов.
// Возвращает 1.0 при норме, >1 при всплеске, <1 при спаде.
// tolerance задаёт зону «нормы» для интерпретации (не влияет на само значение).
func VolumeSignal(volumes []float64, n int, tolerance float64) (float64, error) {
	avg, err := SMA(volumes, n)
	if err != nil {
		return 0, err
	}
	if avg == 0 {
		return 0, fmt.Errorf("%w: средний объём равен 0", ErrInsufficientData)
	}
	return volumes[len(volumes)-1] / avg, nil
}

// --- Интерпретации значений для UI ---

const (
	rsiOverbought = 70 // порог перекупленности
	rsiOversold   = 30 // порог перепроданности
)

// InterpretRSI переводит значение RSI в человекочитаемую фразу.
func InterpretRSI(v float64) string {
	switch {
	case v >= rsiOverbought:
		return "перекупленность"
	case v <= rsiOversold:
		return "перепроданность"
	default:
		return "нейтральная зона"
	}
}

// InterpretROC переводит значение ROC (%) в человекочитаемую фразу.
func InterpretROC(v float64) string {
	switch {
	case v > 0:
		return "растущий тренд"
	case v < 0:
		return "падающий тренд"
	default:
		return "без изменений"
	}
}

// InterpretSMAValue описывает SMA конкретного периода как самостоятельный
// индикатор: где последняя цена относительно этой средней.
func InterpretSMAValue(lastPrice, sma float64, period int) string {
	if sma == 0 {
		return "нет данных"
	}
	switch {
	case lastPrice > sma:
		return fmt.Sprintf("цена выше SMA(%d) — локальная сила", period)
	case lastPrice < sma:
		return fmt.Sprintf("цена ниже SMA(%d) — локальная слабость", period)
	default:
		return fmt.Sprintf("цена на уровне SMA(%d)", period)
	}
}

// InterpretSMA сравнивает краткосрочную и долгосрочную SMA (перекрестие средних).
func InterpretSMA(short, long float64) string {
	switch {
	case short > long:
		return "краткосрочная выше долгосрочной (бычий сигнал)"
	case short < long:
		return "краткосрочная ниже долгосрочной (медвежий сигнал)"
	default:
		return "средние равны"
	}
}

// InterpretVolume переводит отношение объёма в человекочитаемую фразу.
// tolerance — зона нормы вокруг 1.0.
func InterpretVolume(v, tolerance float64) string {
	switch {
	case v > 1+tolerance:
		return "всплеск объёма"
	case v < 1-tolerance:
		return "спад объёма"
	default:
		return "объём в норме"
	}
}
