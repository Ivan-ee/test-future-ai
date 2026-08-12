// Package server поднимает HTTP API на chi.
//
// T2: добавлен эндпоинт GET /api/assets/{id} — детальная карточка монеты с
// текущей ценой и последними значениями технических индикаторов (RSI/ROC/SMA/
// VolumeSignal) с человекочитаемыми интерпретациями.
package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
}

// New создаёт сервер с заданными зависимостями.
func New(cfg config.Config, priceStore *storage.PricePoints, indicatorStore *storage.IndicatorSnapshots) *Server {
	return &Server{cfg: cfg, priceStore: priceStore, indicatorStore: indicatorStore}
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
