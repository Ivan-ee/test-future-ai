package scoring

import (
	"testing"
)

// --- RSI signal ---

func TestRSISignal_Neutral50(t *testing.T) {
	t.Parallel()
	sig, _ := RSISignal(50)
	if sig < -tol || sig > tol {
		t.Errorf("RSI=50: хотели ≈0, получили %v", sig)
	}
}

func TestRSISignal_OverboughtNegative(t *testing.T) {
	t.Parallel()
	sig, _ := RSISignal(80)
	if sig >= 0 {
		t.Errorf("RSI=80 (перекупленность): хотели <0, получили %v", sig)
	}
}

func TestRSISignal_OversoldPositive(t *testing.T) {
	t.Parallel()
	sig, _ := RSISignal(20)
	if sig <= 0 {
		t.Errorf("RSI=20 (перепроданность): хотели >0, получили %v", sig)
	}
}

func TestRSISignal_ClampedToRange(t *testing.T) {
	t.Parallel()
	for _, rsi := range []float64{0, 10, 50, 80, 95, 100} {
		sig, _ := RSISignal(rsi)
		if sig < -1 || sig > 1 {
			t.Errorf("RSI=%v: сигнал %v вне [-1,1]", rsi, sig)
		}
	}
}

// --- Momentum signal ---

func TestMomentumSignal_BullishOnGrowth(t *testing.T) {
	t.Parallel()
	// ROC +8%, SMA7 > SMA20 → сильный вверх.
	sig, _ := MomentumSignal(8, 105, 100)
	if sig <= 0 {
		t.Errorf("бычий моментум: хотели >0, получили %v", sig)
	}
}

func TestMomentumSignal_BearishOnDecline(t *testing.T) {
	t.Parallel()
	sig, _ := MomentumSignal(-8, 95, 100)
	if sig >= 0 {
		t.Errorf("медвежий моментум: хотели <0, получили %v", sig)
	}
}

func TestMomentumSignal_ClampedToRange(t *testing.T) {
	t.Parallel()
	for _, roc := range []float64{-50, -10, 0, 10, 50} {
		sig, _ := MomentumSignal(roc, 100, 100)
		if sig < -1 || sig > 1 {
			t.Errorf("ROC=%v: сигнал %v вне [-1,1]", roc, sig)
		}
	}
}

// --- Volume signal ---

func TestVolumeSignal_SpikePositive(t *testing.T) {
	t.Parallel()
	sig, _ := VolumeSignal(1.8, 0.2)
	if sig <= 0 {
		t.Errorf("всплеск объёма: хотели >0, получили %v", sig)
	}
}

func TestVolumeSignal_LowNegative(t *testing.T) {
	t.Parallel()
	sig, _ := VolumeSignal(0.5, 0.2)
	if sig >= 0 {
		t.Errorf("спад объёма: хотели <0, получили %v", sig)
	}
}

func TestVolumeSignal_NormalNearZero(t *testing.T) {
	t.Parallel()
	sig, _ := VolumeSignal(1.0, 0.2)
	if sig < -tol || sig > tol {
		t.Errorf("объём в норме: хотели ≈0, получили %v", sig)
	}
}

// --- FactorsFromIndicators — сквозная сборка ---

func TestFactorsFromIndicators_ThreeFactors(t *testing.T) {
	t.Parallel()
	in := IndicatorInput{
		RSI:          72,
		ROC:          3.2,
		SMA7:         61000,
		SMA20:        59000,
		VolumeSignal: 1.4,
	}
	factors := FactorsFromIndicators(in, 0.2)
	if len(factors) != 3 {
		t.Fatalf("хотели 3 фактора, получили %d", len(factors))
	}
	for _, f := range factors {
		if f.Signal < -1 || f.Signal > 1 {
			t.Errorf("фактор %s: сигнал %v вне [-1,1]", f.Name, f.Signal)
		}
		if f.Detail == "" {
			t.Errorf("фактор %s: пустой detail", f.Name)
		}
	}
}

// --- сквозной прогноз из индикаторов: согласие факторов ---

func TestForecast_FromIndicators_BullishAgreement(t *testing.T) {
	t.Parallel()
	// RSI=40 (слабо перепродан → вверх), моментум вверх, объём выше среднего.
	in := IndicatorInput{
		RSI:          40,
		ROC:          5,
		SMA7:         105,
		SMA20:        100,
		VolumeSignal: 1.5,
	}
	factors := FactorsFromIndicators(in, 0.2)
	r := Forecast(factors, nil)
	if r.Direction != DirectionUp {
		t.Errorf("согласный бычий набор: хотели up, получили %s", r.Direction)
	}
	if r.Confidence < 0.55 {
		t.Errorf("согласный набор: confidence слишком низкий %v", r.Confidence)
	}
}

// --- T4: SentimentSignal ---

func TestSentimentSignal_PositiveIsPositive(t *testing.T) {
	t.Parallel()
	sig, detail := SentimentSignal(0.6)
	if sig <= 0 {
		t.Errorf("позитивный сентимент 0.6: хотели >0, получили %v", sig)
	}
	if detail == "" {
		t.Error("detail не должен быть пустым")
	}
}

func TestSentimentSignal_NegativeIsNegative(t *testing.T) {
	t.Parallel()
	sig, _ := SentimentSignal(-0.6)
	if sig >= 0 {
		t.Errorf("негативный сентимент -0.6: хотели <0, получили %v", sig)
	}
}

func TestSentimentSignal_ClampedToRange(t *testing.T) {
	t.Parallel()
	for _, s := range []float64{-2, -1.5, 0, 0.8, 1.5, 3} {
		sig, _ := SentimentSignal(s)
		if sig < -1 || sig > 1 {
			t.Errorf("sentiment=%v: сигнал %v вне [-1,1]", s, sig)
		}
	}
}

// --- T4: FactorsFromIndicatorsAndSentiment ---

func TestFactorsFromIndicatorsAndSentiment_WithSentiment(t *testing.T) {
	t.Parallel()
	in := IndicatorInput{RSI: 50, ROC: 1, SMA7: 100, SMA20: 100, VolumeSignal: 1.0}
	factors := FactorsFromIndicatorsAndSentiment(in, 0.5, true, 0.2)
	if len(factors) != 4 {
		t.Fatalf("хотели 4 фактора (с sentiment), получили %d", len(factors))
	}
	if factors[3].Name != FactorSentiment {
		t.Errorf("4-й фактор: хотели sentiment, получили %s", factors[3].Name)
	}
}

func TestFactorsFromIndicatorsAndSentiment_WithoutSentiment(t *testing.T) {
	t.Parallel()
	in := IndicatorInput{RSI: 50, ROC: 1, SMA7: 100, SMA20: 100, VolumeSignal: 1.0}
	factors := FactorsFromIndicatorsAndSentiment(in, 0, false, 0.2)
	if len(factors) != 3 {
		t.Fatalf("хотели 3 фактора (без sentiment), получили %d", len(factors))
	}
}
