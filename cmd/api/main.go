// Package main — точка входа бэкенда test-future.
//
// Порядок запуска: конфиг → открытие БД (с миграцией и сидингом) → запуск
// worker-горутины (опрос CoinGecko по крону) → HTTP-сервер. graceful shutdown
// по SIGINT/SIGTERM.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"test-future/internal/config"
	"test-future/internal/db"
	"test-future/internal/server"
	"test-future/internal/storage"
	"test-future/internal/worker"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("test-future: %v", err)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := cfg.EnsureDBDir(); err != nil {
		return err
	}

	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	log.Printf("открытие БД: %s", cfg.DBPath)
	database, err := db.Open(rootCtx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer func() {
		_ = database.Close()
	}()

	// Репозитории.
	assetsRepo := storage.NewAssets(database)
	sourcesRepo := storage.NewSources(database)
	priceRepo := storage.NewPricePoints(database)
	logRepo := storage.NewUpdateLog(database)
	indicatorRepo := storage.NewIndicatorSnapshots(database)
	forecastRepo := storage.NewForecasts(database)

	// Worker: опрос источников в горутине.
	wkr := worker.New(cfg, assetsRepo, sourcesRepo, priceRepo, logRepo, indicatorRepo, forecastRepo)
	go wkr.Run(rootCtx)

	// HTTP-сервер.
	srv := server.New(cfg, priceRepo, indicatorRepo, forecastRepo, assetsRepo)
	httpServer := &http.Server{
		Addr:              cfg.BackendAddr(),
		Handler:           srv.Router(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Запускаем сервер в горутине, чтобы основной поток ждал сигнал.
	go func() {
		log.Printf("HTTP-сервер слушает на %s", cfg.BackendAddr())
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP-сервер: %v", err)
		}
	}()

	// graceful shutdown по SIGINT/SIGTERM.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Printf("получен сигнал остановки, завершаемся...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rootCancel() // сначала останавливаем worker

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return err
	}
	log.Printf("остановлено")
	return nil
}
