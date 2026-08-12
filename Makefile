# Makefile для test-future: запуск бэкенда (Go) и фронтенда (Next.js).
# `make dev` поднимает оба процесса параллельно.

SHELL := /bin/bash

# Пути.
ROOT      := $(shell pwd)
BACKEND   := $(ROOT)
FRONTEND  := $(ROOT)/frontend
BIN_DIR   := $(ROOT)/bin

# Если .env существует, godotenv подхватит его; иначе берём значения по умолчанию.
BACKEND_PORT  ?= 8081
FRONTEND_PORT ?= 3001

# Экспортируем переменные окружения для дочерних процессов.
export BACKEND_PORT
export FRONTEND_PORT

.DEFAULT_GOAL := help

.PHONY: help
help: ## Показать список команд
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

.PHONY: env
env: ## Создать .env из .env.example (если ещё нет)
	@if [ ! -f .env ]; then cp .env.example .env && echo ".env создан из .env.example"; else echo ".env уже существует"; fi

.PHONY: backend-build
backend-build: ## Собрать Go-бинарник бэкенда
	@mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/api ./cmd/api

.PHONY: build
build: backend-build ## Собрать Go-бинарник (alias)

.PHONY: backend-run
backend-run: ## Запустить бэкенд (go run)
	go run ./cmd/api

.PHONY: frontend-install
frontend-install: ## Установить зависимости фронтенда
	cd $(FRONTEND) && npm install

.PHONY: frontend-dev
frontend-dev: ## Запустить фронтенд в dev-режиме
	cd $(FRONTEND) && npm run dev

.PHONY: db-init
db-init: ## Применить схему БД (запускает бэкенд с миграцией и выходит)
	@echo "Схема применяется автоматически при старте бэкенда. Запускаю миграцию..."
	@DB_PATH ?= data/testfuture.db
	go run ./cmd/api &
	@API_PID=$$!; sleep 2; kill $$API_PID 2>/dev/null || true; \
	echo "Схема применена в $$DB_PATH"

.PHONY: dev
dev: env ## Поднять бэкенд и фронтенд параллельно (Ctrl+C останавливает оба)
	@echo "Запуск dev-режима: бэкенд :$(BACKEND_PORT), фронтенд :$(FRONTEND_PORT)"
	@trap 'kill 0' EXIT; \
	(go run ./cmd/api) & \
	(cd $(FRONTEND) && npm run dev) & \
	wait

.PHONY: test
test: ## Запустить Go-тесты
	go test ./...

.PHONY: test-verbose
test-verbose: ## Запустить Go-тесты с подробным выводом
	go test -v ./...

.PHONY: fmt
fmt: ## Отформатировать Go-код
	go fmt ./...

.PHONY: clean
clean: ## Удалить собранные артефакты и БД
	rm -rf $(BIN_DIR) $(FRONTEND)/.next data/*.db data/*.db-*

.PHONY: smoke
smoke: ## Smoke-тест: чистая БД → бэкенд наполняет данные → проверка API → остановка
	@echo "==> Smoke-тест: запуск с чистой БД"
	@rm -f data/*.db data/*.db-*
	@mkdir -p data
	@go run ./cmd/api & API_PID=$$!; \
	echo "    бэкенд запущен (PID $$API_PID), ждём наполнения БД..."; \
	sleep 45; \
	echo "==> Проверка /api/assets:"; \
	curl -sf http://localhost:$(BACKEND_PORT)/api/assets | \
		python3 -c "import sys,json; d=json.load(sys.stdin); print('    монет в списке:', len(d)); \
		[print('     ', x.get('symbol','?'), '$'+str(round(x.get('price_usd',0),2))) for x in d[:5]]" \
		|| { echo "    ОШИБКА: /api/assets не вернул данные"; kill $$API_PID; exit 1; }; \
	echo "==> Проверка /api/forecasts:"; \
	curl -sf http://localhost:$(BACKEND_PORT)/api/forecasts | \
		python3 -c "import sys,json; d=json.load(sys.stdin); print('    прогнозов:', len(d)); \
		[print('     ', x.get('symbol','?'), x.get('direction','?'), 'conf='+str(round(x.get('confidence',0),2))) for x in d[:5]]" \
		|| { echo "    ПРЕДУПРЕЖДЕНИЕ: прогнозы ещё не посчитаны (индикаторы могут копиться)"; }; \
	echo "==> Проверка /api/health:"; \
	curl -sf http://localhost:$(BACKEND_PORT)/api/health && echo ""; \
	echo "==> Smoke-тест пройден, останавливаю бэкенд"; \
	kill $$API_PID 2>/dev/null || true; wait $$API_PID 2>/dev/null || true
