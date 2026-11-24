SHELL := /bin/bash
GO ?= go

.DEFAULT_GOAL := help

.PHONY: help build run test race fmt vet tidy verify up up-full down ps logs image

help: ## Muestra esta ayuda
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'

build: ## Compila todos los binarios
	$(GO) build ./...

run: ## Ejecuta el servidor HTTP en el host (carga .env si existe)
	@set -a; [ -f .env ] && . ./.env; set +a; \
		$(GO) run ./cmd/api

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

image: ## Construye la imagen Docker del backend
	docker compose --profile app build api

down: ## Detiene los servicios locales
	docker compose down

ps: ## Muestra el estado de los servicios locales
	docker compose ps

logs: ## Sigue los registros de los servicios locales
	docker compose logs -f
