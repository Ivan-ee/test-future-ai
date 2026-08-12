// Файл signals.go — мост между сырыми индикаторами (IndicatorSnapshot) и
// факторами прогноза (scoring.Factor). Каждая функция переводит значение
// индикатора в сигнал [-1..1] и человекочитаемый detail для argument_text.
//
// Логика нормирована так, чтобы типичные рыночные ситуации давали умеренные
// сигналы, а крайние случаи (перекупленность/сильный рост) — близкие к ±1.

package scoring

import (
	"fmt"
	"math"
)

// IndicatorInput — сырые значения индикаторов, из которых считаются сигналы.
// Совпадает по полям с model.IndicatorSnapshot (без служебных полей).
type IndicatorInput struct {
	RSI          float64 // 0..100
	ROC          float64 // проценты
	SMA7         float64
	SMA20        float64
	VolumeSignal float64 // отношение к среднему, ~1.0 = норма
}

// RSI signal: RSI=50 → 0 (нейтрально); RSI≥70 → отрицательный (перекупленность,
// ожидается откат вниз); RSI≤30 → положительный (перепроданность, ожидается
// отскок вверх). Между порогами — линейная интерполяция.
func RSISignal(rsi float64) (float64, string) {
	const (
		overbought = 70.0
		oversold   = 30.0
		mid        = 50.0
	)
	var signal float64
	switch {
	case rsi >= overbought:
		// 70..100 → -(rsi-70)/30, зажимаем в [-1..0]
		signal = -math.Min((rsi-overbought)/(100-overbought), 1.0)
	case rsi <= oversold:
		// 0..30 → +(30-rsi)/30, зажимаем в [0..1]
		signal = math.Min((oversold-rsi)/oversold, 1.0)
	default:
		// 30..50 → +(50-rsi)/20 (слабый вверх), 50..70 → -(rsi-50)/20 (слабый вниз)
		signal = (mid - rsi) / (overbought - mid) // знак определяет сторона от 50
		if signal > 0.5 {
			signal = 0.5
		}
		if signal < -0.5 {
			signal = -0.5
		}
	}
	detail := fmt.Sprintf("RSI=%.1f (%s)", rsi, rsiInterpretation(rsi))
	return clamp(signal), detail
}

func rsiInterpretation(rsi float64) string {
	switch {
	case rsi >= 70:
		return "перекупленность → вниз"
	case rsi <= 30:
		return "перепроданность → вверх"
	default:
		return "нейтрально"
	}
}

// Momentum signal: объединяет ROC и кроссовер SMA(7)/SMA(20). Положительный
// моментум — цена растёт (ROC>0) и краткосрочная SMA выше долгосрочной.
// Сигнал нормируется силой ROC (типичный дневной ROC в пределах ±10%).
func MomentumSignal(roc, sma7, sma20 float64) (float64, string) {
	// ROC-компонент: +10% → ~+1, -10% → ~-1, с мягким насыщением через tanh.
	rocSignal := math.Tanh(roc / 5.0) // /5 чтобы ±5% давало ≈±0.76

	// SMA-кроссовер: +0.5 если SMA7 > SMA20 (бычий), -0.5 если медвежий.
	smaSignal := 0.0
	smaNote := "средние равны"
	if sma20 > 0 {
		ratio := (sma7 - sma20) / sma20
		smaSignal = math.Tanh(ratio * 10) // нормируем относительно типичного разброса
		if sma7 > sma20 {
			smaNote = fmt.Sprintf("SMA(7)>SMA(20) → вверх (расхождение %.2f%%)", ratio*100)
		} else if sma7 < sma20 {
			smaNote = fmt.Sprintf("SMA(7)<SMA(20) → вниз (расхождение %.2f%%)", ratio*100)
		}
	}

	// Среднее двух компонентов: ROC и SMA смотрят на моментум с разных сторон.
	signal := (rocSignal + smaSignal) / 2
	detail := fmt.Sprintf("моментум ROC=%.2f%% и %s", roc, smaNote)
	return clamp(signal), detail
}

// Volume signal: всплеск объёма (VolumeSignal > 1+tolerance) подтверждает
// действующий тренд — но сам по себе объём не указывает направление, поэтому
// сигнал по объёму умеренный. Выше среднего → слабый «вверх» (интерес растёт),
// ниже среднего → слабый «вниз» (интерес падает). Норма → ~0.
func VolumeSignal(volSignal, tolerance float64) (float64, string) {
	if tolerance <= 0 {
		tolerance = 0.2
	}
	var signal float64
	note := "объём в норме"
	switch {
	case volSignal > 1+tolerance:
		// всплеск: чем сильнее, тем выше сигнал, насыщение на +0.5
		signal = math.Min((volSignal-(1+tolerance))*2, 0.5)
		note = fmt.Sprintf("объём выше среднего (%.2f×) → интерес растёт", volSignal)
	case volSignal < 1-tolerance && volSignal > 0:
		signal = math.Max(-((1-tolerance)-volSignal)*2, -0.5)
		note = fmt.Sprintf("объём ниже среднего (%.2f×) → интерес падает", volSignal)
	default:
		signal = 0
	}
	return clamp(signal), note
}

// FactorsFromIndicators собирает три фактора (rsi, momentum, volume) из сырых
// индикаторов — единая точка преобразования IndicatorSnapshot → []Factor для
// scoring.Forecast.
func FactorsFromIndicators(in IndicatorInput, volumeTolerance float64) []Factor {
	rsiSig, rsiDetail := RSISignal(in.RSI)
	momSig, momDetail := MomentumSignal(in.ROC, in.SMA7, in.SMA20)
	volSig, volDetail := VolumeSignal(in.VolumeSignal, volumeTolerance)

	return []Factor{
		{Name: FactorRSI, Signal: rsiSig, Detail: rsiDetail},
		{Name: FactorMomentum, Signal: momSig, Detail: momDetail},
		{Name: FactorVolume, Signal: volSig, Detail: volDetail},
	}
}

// clamp зажимает сигнал в [-1..1].
func clamp(v float64) float64 {
	if v > 1 {
		return 1
	}
	if v < -1 {
		return -1
	}
	return v
}
