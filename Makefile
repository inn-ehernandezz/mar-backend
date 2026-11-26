SHELL := /bin/bash
GO ?= go
DOCUMENTS_COMPOSE := docker compose -f compose.yaml -f compose.documents.yaml --profile app --profile messaging --profile documents
QUERIES_COMPOSE := $(DOCUMENTS_COMPOSE) -f compose.queries.yaml -f compose.queries.gpu.yaml --profile queries
ALL_QUERY_MODELS := $(QUERIES_COMPOSE) --profile query-gpu --profile query-backup

.DEFAULT_GOAL := help

.PHONY: help build run run-worker test race fmt vet tidy verify up up-full up-ingestion up-documents up-document-models stop-documents documents-ps documents-logs up-queries stop-queries queries-ps down ps logs image

help: ## Muestra esta ayuda
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'

build: ## Compila todos los binarios
	$(GO) build ./...

run: ## Ejecuta el servidor HTTP en el host (carga .env si existe)
	@set -a; [ -f .env ] && . ./.env; set +a; \
		$(GO) run ./cmd/api

run-worker: ## Ejecuta el worker de ingesta en el host (carga .env si existe)
	@set -a; [ -f .env ] && . ./.env; set +a; \
		$(GO) run ./cmd/worker

test: ## Ejecuta todas las pruebas
	$(GO) test ./...

race: ## Ejecuta las pruebas con el detector de carreras
	$(GO) test -race ./...

fmt: ## Formatea el código con gofmt
	gofmt -w .

vet: ## Analiza el código con go vet
	$(GO) vet ./...

tidy: ## Sincroniza go.mod y go.sum
	$(GO) mod tidy

verify: fmt vet test ## Formatea, analiza y prueba (uso en CI)

up: ## Levanta la infraestructura local (Qdrant + Azurite)
	docker compose up -d

up-full: image ## Construye y levanta la pila completa, incluido el backend Go
	docker compose --profile app up -d

up-ingestion: image ## Levanta la pila completa con Service Bus y worker
	MAR_SERVICEBUS_ENABLED=true docker compose --profile app --profile messaging up -d

up-documents: ## Levanta la fase documental con detección de GPU para BGE-M3
	bash deploy/docker/start-documents.sh all $(DOCUMENTS_COMPOSE)

up-document-models: ## Levanta el extractor y BGE-M3 con detección de GPU
	bash deploy/docker/start-documents.sh models $(DOCUMENTS_COMPOSE)

stop-documents: ## Detiene la fase documental conservando contenedores y datos
	$(DOCUMENTS_COMPOSE) stop

documents-ps: ## Muestra el estado de la pila documental
	$(DOCUMENTS_COMPOSE) ps

documents-logs: ## Sigue los registros del worker, el extractor y el modelo
	$(DOCUMENTS_COMPOSE) logs -f api worker media embeddings

up-queries: ## Levanta la fase 5 con preferencia por GPU y respaldo en CPU
	bash deploy/docker/start-documents.sh queries $(QUERIES_COMPOSE)

stop-queries: ## Detiene toda la pila de la fase 5 conservando los datos
	$(ALL_QUERY_MODELS) stop

queries-ps: ## Muestra el estado de la pila de la fase 5
	$(ALL_QUERY_MODELS) ps

image: ## Construye la imagen Docker del backend
	docker compose --profile app build api

down: ## Detiene los servicios locales
	docker compose --profile app --profile messaging down

ps: ## Muestra el estado de los servicios locales
	docker compose --profile app --profile messaging ps

logs: ## Sigue los registros de los servicios locales
	docker compose --profile app --profile messaging logs -f
