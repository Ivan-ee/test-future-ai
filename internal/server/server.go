// Package server поднимает HTTP API на chi.
//
// В T1 один эндпоинт: GET /api/assets возвращает список монет с последней
// ценой. CORS пускает фронтенд (по умолчанию localhost:3001). Health-чек
// /api/health удобен для smoke-проверки, что бэкенд жив.
package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"test-future/internal/config"
	"test-future/internal/model"
	"test-future/internal/storage"
)

// Server хранит зависимости HTTP-слоя.
type Server struct {
	cfg        config.Config
	priceStore *storage.PricePoints
}

// New создаёт сервер с заданными зависимостями.
func New(cfg config.Config, priceStore *storage.PricePoints) *Server {
	return &Server{cfg: cfg, priceStore: priceStore}
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

// writeJSON кодирует ответ и ставит заголовки. При ошибке кодирования логируем —
// клиенту уже отправлен статус.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
