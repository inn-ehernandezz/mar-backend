# syntax=docker/dockerfile:1

# Imagen del servidor HTTP de MAR (cmd/api). La compilación es estática
# (CGO_ENABLED=0) para poder ejecutarse sobre una base mínima.

# --- etapa de compilación ---
FROM golang:1.27-alpine AS build

WORKDIR /src

# Los ficheros de módulo se copian antes que el resto para aprovechar la caché
# de capas: cambiar el código no invalida la descarga de dependencias.
COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/mar-api ./cmd/api && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/mar-worker ./cmd/worker

# --- etapa de ejecución ---
FROM alpine:3.22

RUN apk add --no-cache ca-certificates && \
    adduser -D -u 10001 mar

COPY --from=build /out/mar-api /out/mar-worker /usr/local/bin/

USER mar
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/mar-api"]
