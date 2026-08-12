package indicator

import (
	"math"
	"testing"
)

// Пороги сравнения float64 — индикаторы численно устойчивы, но не точны до бита.
const (
	floatTolerance = 1e-6
	rsiNear100     = 0.5 // RSI монотонного ряда близок к 100, но не строго
	// Чередующийся ряд 100,101,100,... при сглаживании Уайлдера даёт RSI чуть
	// выше 50 (последнее изменение — рост). Допуск 3.0 покрывает этот эффект.
	rsiNear50 = 3.0
)

func approx(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s: хотели ≈%v (допуск %v), получили %v", name, want, tol, got)
	}
}

// монотонно растущий ряд из n+1 точки (1, 2, ..., n+1).
func monotonic(n int) []float64 {
	c := make([]float64, n+1)
	for i := range c {
		c[i] = float64(i + 1)
	}
	return c
}

// --- SMA ---

func TestSMA_LastNElements(t *testing.T) {
	t.Parallel()
	v, err := SMA([]float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, 3)
	if err != nil {
		t.Fatalf("SMA: %v", err)
	}
	approx(t, "SMA(последние 3)", v, 9, floatTolerance) // (8+9+10)/3
}

func TestSMA_FullLength(t *testing.T) {
	t.Parallel()
	v, err := SMA([]float64{2, 4, 6}, 3)
	if err != nil {
		t.Fatalf("SMA: %v", err)
	}
	approx(t, "SMA(все)", v, 4, floatTolerance)
}

func TestSMA_InsufficientData(t *testing.T) {
	t.Parallel()
	if _, err := SMA([]float64{1, 2}, 5); err == nil {
		t.Fatal("ожидали ошибку при нехватке данных")
	}
}

func TestSMA_InvalidPeriod(t *testing.T) {
	t.Parallel()
	if _, err := SMA([]float64{1, 2, 3}, 0); err == nil {
		t.Fatal("ожидали ошибку при n=0")
	}
	if _, err := SMA([]float64{1, 2, 3}, -1); err == nil {
		t.Fatal("ожидали ошибку при n<0")
	}
}

// --- ROC ---

func TestROC_PositiveOnGrowth(t *testing.T) {
	t.Parallel()
	// цена выросла с 100 до 120 за 1 период → +20%.
	v, err := ROC([]float64{100, 120}, 1)
	if err != nil {
		t.Fatalf("ROC: %v", err)
	}
	approx(t, "ROC", v, 20, floatTolerance)
}

func TestROC_NegativeOnDecline(t *testing.T) {
	t.Parallel()
	v, err := ROC([]float64{100, 50, 50, 90}, 3)
	if err != nil {
		t.Fatalf("ROC: %v", err)
	}
	approx(t, "ROC", v, -10, floatTolerance) // (90-100)/100*100
}

func TestROC_InsufficientData(t *testing.T) {
	t.Parallel()
	if _, err := ROC([]float64{100}, 1); err == nil {
		t.Fatal("ожидали ошибку при нехватке данных")
	}
}

func TestROC_ZeroPrevious(t *testing.T) {
	t.Parallel()
	if _, err := ROC([]float64{0, 10, 20}, 2); err == nil {
		t.Fatal("ожидали ошибку при предыдущей цене = 0")
	}
}

// --- RSI ---

// TestRSI_MonotonicRise — монотонно растущий ряд (только прибыль) → RSI ≈ 100.
func TestRSI_MonotonicRise(t *testing.T) {
	t.Parallel()
	closes := monotonic(20) // 21 точка
	v, err := RSI(closes, 14)
	if err != nil {
		t.Fatalf("RSI: %v", err)
	}
	if v < 100-rsiNear100 {
		t.Errorf("RSI монотонного ряда: хотели ≈100, получили %v", v)
	}
}

// TestRSI_AlternatingAround50 — чередующийся ряд (равные вверх/вниз) → RSI ≈ 50.
func TestRSI_AlternatingAround50(t *testing.T) {
	t.Parallel()
	// Ряд 100, 101, 100, 101, ... — равные движения вверх и вниз.
	closes := make([]float64, 30)
	for i := range closes {
		if i%2 == 0 {
			closes[i] = 100
		} else {
			closes[i] = 101
		}
	}
	v, err := RSI(closes, 14)
	if err != nil {
		t.Fatalf("RSI: %v", err)
	}
	approx(t, "RSI чередующегося ряда", v, 50, rsiNear50)
}

// TestRSI_OnlyDecline — строго падающий ряд → RSI ≈ 0.
func TestRSI_OnlyDecline(t *testing.T) {
	t.Parallel()
	closes := make([]float64, 21)
	for i := range closes {
		closes[i] = float64(21 - i) // 21, 20, ..., 1
	}
	v, err := RSI(closes, 14)
	if err != nil {
		t.Fatalf("RSI: %v", err)
	}
	if v > rsiNear100 {
		t.Errorf("RSI падающего ряда: хотели ≈0, получили %v", v)
	}
}

// TestRSI_ConstantSeries — постоянный ряд (нет изменений) → RSI = 50.
func TestRSI_ConstantSeries(t *testing.T) {
	t.Parallel()
	closes := make([]float64, 20)
	for i := range closes {
		closes[i] = 42
	}
	v, err := RSI(closes, 14)
	if err != nil {
		t.Fatalf("RSI: %v", err)
	}
	approx(t, "RSI постоянного ряда", v, 50, floatTolerance)
}

func TestRSI_InsufficientData(t *testing.T) {
	t.Parallel()
	if _, err := RSI([]float64{1, 2, 3}, 14); err == nil {
		t.Fatal("ожидали ошибку при нехватке данных")
	}
}

// --- VolumeSignal ---

func TestVolumeSignal_SpikeAboveOne(t *testing.T) {
	t.Parallel()
	// 14 точек по 100, последняя — 500 → всплеск.
	volumes := make([]float64, 15)
	for i := range volumes {
		volumes[i] = 100
	}
	volumes[14] = 500
	v, err := VolumeSignal(volumes, 14, 0.2)
	if err != nil {
		t.Fatalf("VolumeSignal: %v", err)
	}
	if v <= 1 {
		t.Errorf("всплеск объёма: хотели >1, получили %v", v)
	}
}

func TestVolumeSignal_LowBelowOne(t *testing.T) {
	t.Parallel()
	volumes := make([]float64, 15)
	for i := range volumes {
		volumes[i] = 100
	}
	volumes[14] = 10 // спад
	v, err := VolumeSignal(volumes, 14, 0.2)
	if err != nil {
		t.Fatalf("VolumeSignal: %v", err)
	}
	if v >= 1 {
		t.Errorf("спад объёма: хотели <1, получили %v", v)
	}
}

func TestVolumeSignal_NormalAroundOne(t *testing.T) {
	t.Parallel()
	volumes := make([]float64, 15)
	for i := range volumes {
		volumes[i] = 100
	}
	v, err := VolumeSignal(volumes, 14, 0.2)
	if err != nil {
		t.Fatalf("VolumeSignal: %v", err)
	}
	approx(t, "VolumeSignal норма", v, 1, floatTolerance)
}

func TestVolumeSignal_ZeroAverage(t *testing.T) {
	t.Parallel()
	volumes := make([]float64, 15) // все нули
	if _, err := VolumeSignal(volumes, 14, 0.2); err == nil {
		t.Fatal("ожидали ошибку при нулевом среднем объёме")
	}
}

// --- Комбинированный сценарий по критериям приёмки ---

// TestMonotonicRise_AllIndicators — монотонно растущий ряд: RSI≈100, ROC>0,
// SMA(7) > SMA(20). Прямая проверка условия из спеки.
func TestMonotonicRise_AllIndicators(t *testing.T) {
	t.Parallel()
	closes := monotonic(20) // 21 точка: 1..21

	rsi, err := RSI(closes, 14)
	if err != nil {
		t.Fatalf("RSI: %v", err)
	}
	roc, err := ROC(closes, 10)
	if err != nil {
		t.Fatalf("ROC: %v", err)
	}
	sma7, err := SMA(closes, 7)
	if err != nil {
		t.Fatalf("SMA(7): %v", err)
	}
	sma20, err := SMA(closes, 20)
	if err != nil {
		t.Fatalf("SMA(20): %v", err)
	}

	if rsi < 100-rsiNear100 {
		t.Errorf("RSI: хотели ≈100, получили %v", rsi)
	}
	if roc <= 0 {
		t.Errorf("ROC: хотели >0, получили %v", roc)
	}
	if sma7 <= sma20 {
		t.Errorf("хотели SMA(7)>SMA(20): получили %v и %v", sma7, sma20)
	}
}
