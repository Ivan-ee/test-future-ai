// Package server поднимает HTTP API на chi.
//
// T2: добавлен эндпоинт GET /api/assets/{id} — детальная карточка монеты с
// текущей ценой и последними значениями технических индикаторов (RSI/ROC/SMA/
// VolumeSignal) с человекочитаемыми интерпретациями.
//
// T3: добавлены эндпоинты прогнозов:
//   - GET /api/forecasts — последние active-прогнозы по всем монетам (для
//     главной страницы: стрелка ↑/↓ + confidence).
//   - GET /api/forecasts/{asset} — детальная карточка прогноза: направление,
//     уверенность, риск, декомпозиция по факторам и использованные данные.
//     Параметр {asset} — id актива (как в /api/assets/{id}).
package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"test-future/internal/config"
	"test-future/internal/indicator"
	"test-future/internal/model"
	"test-future/internal/storage"
)

// Server хранит зависимости HTTP-слоя.
type Server struct {
	cfg            config.Config
	priceStore     *storage.PricePoints
	indicatorStore *storage.IndicatorSnapshots
	forecastStore  *storage.Forecasts
	assetsStore    *storage.Assets
	newsStore      *storage.NewsItems
}

// New создаёт сервер с заданными зависимостями.
func New(
	cfg config.Config,
	priceStore *storage.PricePoints,
	indicatorStore *storage.IndicatorSnapshots,
	forecastStore *storage.Forecasts,
	assetsStore *storage.Assets,
	newsStore *storage.NewsItems,
) *Server {
	return &Server{
		cfg:            cfg,
		priceStore:     priceStore,
		indicatorStore: indicatorStore,
		forecastStore:  forecastStore,
		assetsStore:    assetsStore,
		newsStore:      newsStore,
	}
}

// Router собирает маршрутизатор chi с middleware и обработчиками.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(15 * time.Second))

	// CORS: пускаем фронтенд. В прототипе — конкретный origin + localhost.
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins: []string{
			s.cfg.FrontendURL(),
			"http://127.0.0.1:" + strconv.Itoa(s.cfg.FrontendPort),
		},
		AllowedMethods:   []string{http.MethodGet, http.MethodOptions},
		AllowedHeaders:   []string{"Accept", "Content-Type"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	r.Route("/api", func(r chi.Router) {
		r.Get("/health", s.handleHealth)
		r.Get("/assets", s.handleAssets)
		r.Get("/assets/{id}", s.handleAssetDetail)
		r.Get("/forecasts", s.handleForecasts)
		r.Get("/forecasts/{asset}", s.handleForecastDetail)
	})

	return r
}

// handleHealth — простой liveness-чек.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleAssets возвращает список монет с последней ценой.
func (s *Server) handleAssets(w http.ResponseWriter, r *http.Request) {
	items, err := s.priceStore.LatestByAsset(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "не удалось получить активы",
		})
		return
	}
	if items == nil {
		items = []model.AssetPrice{} // никогда не null в ответе
	}
	writeJSON(w, http.StatusOK, items)
}

// handleAssetDetail возвращает детальную карточку монеты: текущую цену и
// последние значения индикаторов с человекочитаемыми интерпретациями.
// 404, если актив не найден; индикаторы опциональны (null, если ещё не посчитаны).
func (s *Server) handleAssetDetail(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный id актива"})
		return
	}

	price, err := s.priceStore.LatestByAssetID(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "актив не найден"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "не удалось получить актив"})
		return
	}

	detail := model.AssetDetail{AssetPrice: price, Indicators: s.indicatorsView(r.Context(), id, price.PriceUSD)}
	writeJSON(w, http.StatusOK, detail)
}

// handleForecasts возвращает последние active-прогнозы по всем монетам —
// DTO для главной страницы и списка прогнозов.
func (s *Server) handleForecasts(w http.ResponseWriter, r *http.Request) {
	items, err := s.forecastStore.LatestAll(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "не удалось получить прогнозы"})
		return
	}
	if items == nil {
		items = []model.ForecastSummary{}
	}
	writeJSON(w, http.StatusOK, items)
}

// handleForecastDetail возвращает детальную карточку прогноза по активу:
// направление, уверенность, риск, аргументацию, декомпозицию по факторам и
// использованные данные (цена + индикаторы). 404, если актив или прогноз не найдены.
func (s *Server) handleForecastDetail(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "asset")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный id актива"})
		return
	}

	// Нужна цена (для Data.PriceUSD) и метаданные актива (symbol, name).
	price, err := s.priceStore.LatestByAssetID(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "актив не найден"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "не удалось получить актив"})
		return
	}

	forecast, factors, err := s.forecastStore.LatestByAsset(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "прогноз ещё не посчитан"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "не удалось получить прогноз"})
		return
	}

	view := s.forecastView(r.Context(), forecast, factors, price)
	writeJSON(w, http.StatusOK, view)
}

// forecastView собирает DTO прогноза: доменный прогноз + факторы + «использованные
// данные» (цена и значения индикаторов из снапшота) + последние новости с сентиментом.
func (s *Server) forecastView(ctx context.Context, f model.Forecast, factors []model.ForecastFactor, price model.AssetPrice) model.ForecastView {
	factorViews := make([]model.ForecastFactorView, 0, len(factors))
	for _, fc := range factors {
		factorViews = append(factorViews, model.ForecastFactorView{
			Name:           fc.Name,
			Signal:         fc.Signal,
			BaseWeight:     fc.BaseWeight,
			AdjustedWeight: fc.AdjustedWeight,
			Contribution:   fc.Contribution,
			Detail:         fc.Detail,
		})
	}

	// Использованные данные: цена из последней точки, индикаторы — из снапшота.
	// Если индикаторов нет (маловероятно при наличии прогноза) — calculated_at nil.
	data := model.ForecastDataView{
		PriceUSD:     price.PriceUSD,
		Change24H:    price.Change24H,
		CalculatedAt: nil,
	}
	snap, err := s.indicatorStore.ByAsset(ctx, price.AssetID)
	if err == nil {
		data.RSI = snap.RSI
		data.ROC = snap.ROC
		data.SMA7 = snap.SMA7
		data.SMA20 = snap.SMA20
		data.VolumeSignal = snap.VolumeSignal
		calculatedAt := snap.CalculatedAt
		data.CalculatedAt = &calculatedAt
	}

	// T4: последние новости по монете за 24ч с сентимент-скором и summary.
	news := s.newsViews(ctx, price.AssetID)

	return model.ForecastView{
		AssetID:      f.AssetID,
		Symbol:       price.Symbol,
		Name:         price.Name,
		CreatedAt:    f.CreatedAt,
		HorizonHours: f.HorizonHours,
		Direction:    f.Direction,
		Confidence:   f.Confidence,
		RiskNote:     f.RiskNote,
		ArgumentText: f.ArgumentText,
		RawScore:     f.RawScore,
		Factors:      factorViews,
		Data:         data,
		News:         news,
	}
}

// newsViews возвращает последние новости по монете за 24ч в виде DTO. Если
// новостей нет — пустой слайс (не null).
func (s *Server) newsViews(ctx context.Context, assetID int64) []model.NewsItemView {
	since := time.Now().UTC().Add(-24 * time.Hour)
	items, err := s.newsStore.RecentByAsset(ctx, assetID, since, 10)
	if err != nil {
		// Не валить ответ из-за новостей — прогноз отдаётся без них.
		log.Printf("server: новости для asset %d: %v", assetID, err)
		return []model.NewsItemView{}
	}
	views := make([]model.NewsItemView, 0, len(items))
	for _, n := range items {
		views = append(views, model.NewsItemView{
			Title:            n.Title,
			Link:             n.Link,
			PublishedAt:      n.PublishedAt,
			SentimentScore:   n.SentimentScore,
			SentimentSummary: n.SentimentSummary,
		})
	}
	return views
}

// indicatorsView собирает представление индикаторов по активу: читает снапшот
// из БД и проставляет интерпретации. lastPrice нужен, чтобы описать положение
// цены относительно SMA. Если данных ещё нет — пустые значения и nil calculated_at.
func (s *Server) indicatorsView(ctx context.Context, assetID int64, lastPrice float64) model.IndicatorsView {
	snap, err := s.indicatorStore.ByAsset(ctx, assetID)
	if err != nil {
		// Данных ещё нет (worker не успел посчитать) — отдаём пустые значения.
		return model.IndicatorsView{}
	}
	calculatedAt := snap.CalculatedAt
	return model.IndicatorsView{
		RSI: model.IndicatorValue{
			Value:          snap.RSI,
			Interpretation: indicator.InterpretRSI(snap.RSI),
		},
		ROC: model.IndicatorValue{
			Value:          snap.ROC,
			Interpretation: indicator.InterpretROC(snap.ROC),
		},
		SMA7: model.IndicatorValue{
			Value:          snap.SMA7,
			Interpretation: indicator.InterpretSMAValue(lastPrice, snap.SMA7, 7),
		},
		SMA20: model.IndicatorValue{
			Value:          snap.SMA20,
			Interpretation: indicator.InterpretSMAValue(lastPrice, snap.SMA20, 20),
		},
		VolumeSignal: model.IndicatorValue{
			Value:          snap.VolumeSignal,
			Interpretation: indicator.InterpretVolume(snap.VolumeSignal, indicator.DefaultVolumeTolerance),
		},
		CalculatedAt: &calculatedAt,
	}
}

// writeJSON кодирует ответ и ставит заголовки. При ошибке кодирования логируем —
// клиенту уже отправлен статус.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
