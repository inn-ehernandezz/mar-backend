# MAR — Roadmap de implementación

> Última actualización: 2026-11-25
> Convención de estados: `[ ]` pendiente · `[~]` en curso · `[x]` completado · `[!]` bloqueado

## Estado global

| Fase | Descripción | Estado |
|---|---|---|
| 1 | Infraestructura base (Go modular, HTTP, configuración, logging, errores) | `[x]` Completado |
| 2 | Persistencia (Qdrant, Azure Blob Storage, Azure Table Storage) | `[x]` Completado |
| 3 | Ingesta y procesamiento asíncrono (Service Bus, jobs, idempotencia) | `[x]` Completado |
| 4 | Procesamiento documental e IA (worker Python, Docling, BGE-M3, indexado) | `[x]` Completado |
| 5 | Agentic RAG (Qwen3, herramientas, reranker, referencias) | `[ ]` Pendiente |
| 6 | Procesamiento multimodal (FFmpeg, OpenCV, faster-whisper, Qwen3-VL, Cosmos-Embed1) | `[ ]` Pendiente |
| 7 | Evolución hacia microservicios | `[ ]` Pendiente (no iniciar sin necesidad real) |

---

## Fase 1 — Infraestructura base

Estado: **completada**. Verificación local: `gofmt`, `go vet`, `go build` y
`go test` en verde; arranque real del servidor con comprobación de sondas y rutas.

- [x] Revisar el repositorio existente (Go 1.27.4, Gin v1.12.0; `qdrant/go-client` v1.20.2)
- [x] Actualizar `docs/ARCHITECTURE.md` con las decisiones de la revisión
- [x] Documentar decisiones arquitectónicas mediante ADR (`docs/adr/0001`–`0006`)
- [x] Ordenar la estructura modular de Go (`cmd/api`, `internal/platform/`)
- [x] Implementar configuración tipada y validada (`internal/platform/config`)
- [x] Implementar logging estructurado con `log/slog` (`internal/platform/logging`)
- [x] Implementar middleware de correlación (`request_id`, `tenant_id`, `job_id`)
- [x] Implementar taxonomía de errores y su mapeo HTTP (`internal/platform/apierr`)
- [x] Generador de identificadores UUID v4 (`internal/platform/ids`)
- [x] Implementar servidor HTTP con timeouts y cierre ordenado (`internal/platform/httpapi`)
- [x] Implementar sondas `/health/live` y `/health/ready` con registro de checks
- [x] Declarar las rutas de `/v1`; las no implementadas responden `501 Not Implemented`
- [x] Exigir `X-Tenant-ID` en todo `/v1` (sin él, `401`): requisito transversal
- [x] Crear `compose.yaml`, `Makefile`, `.env.example`, `.gitignore` y `README.md`
- [x] Contrato OpenAPI inicial (`contracts/openapi/openapi.yaml`)
- [x] Pruebas unitarias de configuración, errores, identificadores, salud y transporte HTTP

### Versiones verificadas (2026-11-24)

Fijadas al integrar cada dependencia. Las de Azure no se añaden a `go.mod`
hasta que el código las importe: así se evitan dependencias sin uso (que
`go mod tidy` retiraría).

| Componente | Versión | Notas |
|---|---|---|
| Go | 1.27.4 | `go.mod` |
| Gin | v1.12.0 | router HTTP |
| `qdrant/go-client` | v1.20.2 | cliente gRPC; mismo `minor` que el servidor Qdrant |
| `grpc` | v1.85.0 | dependencia directa del cliente Qdrant |
| Qdrant (imagen) | `qdrant/qdrant:v1.20.2` | `compose.yaml` |
| Azurite (imagen) | `mcr.microsoft.com/azure-storage/azurite:3.37.0` | emulador Blob/Queue/Table |
| `aztables` | v1.4.1 | importado en la Fase 2 |
| `azservicebus` | v1.11.0 | Fase 3 |
| `azidentity` | v1.15.0 | identidad administrada en Azure |
| `azblob` | v1.8.3 | importado en la Fase 2 (v1.9.0-beta.1 es preliminar) |

### Notas de implementación

- `/v1` exige `X-Tenant-ID` antes de atender la ruta: sin cabecera devuelve
  `401`; con ella, las rutas no implementadas devuelven `501`.
- El error del middleware de tenant (`401`) y el `501` de las rutas usan el
  mismo esquema (`code`, `message`, `details`, `request_id`).
- El código de error `method_not_allowed` (`405`) se añadió a la taxonomía.

## Fase 2 — Persistencia

Estado: **completada**. Verificación local con servicios reales en marcha
(Qdrant `v1.20.2` y Azurite `3.37.0`): `gofmt`, `go vet`, `go build`,
`go test ./...` y `go test -race ./...` en verde, incluida la batería de
pruebas de integración, y arranque real del servidor con `GET /health/ready`
devolviendo `200` con las tres dependencias y `503` al detener Azurite.

- [x] Puertos de almacenamiento independientes de SDK (`internal/storage`)
- [x] Cliente oficial de Qdrant sobre gRPC (`internal/storage/qdrant`)
- [x] Configuración y gestión idempotente de colecciones (`text_embeddings`)
- [x] Colección `visual_embeddings` condicionada a dimensión válida (pendiente de confirmar)
- [x] Índices de payload (`tenant_id`, `resource_id`, `content_type`)
- [x] Qdrant: upsert, recuperación, búsqueda, filtrado, borrado y paginación (`Scroll`), aislados por tenant
- [x] Azure Blob Storage, contenedores `raw`, `derived`, `frames`, `transcripts` (`internal/storage/blob`)
- [x] Blob: subida en streaming, integridad SHA-256, prevención de sobrescrituras e idempotencia
- [x] Azure Table Storage (`internal/storage/table`): tablas `resources` y `jobs`, `PartitionKey = tenant_id`
- [x] Repositorio de recursos: creación idempotente, recuperación, actualización y listado paginado
- [x] Repositorio de trabajos: máquina de estados (`PENDING`→`QUEUED`→`PROCESSING`→`COMPLETED`/`FAILED`/`CANCELLED`)
- [x] Concurrencia optimista mediante ETag; conflicto ante ETag obsoleto
- [x] Sondas de disponibilidad reales cableadas en `/health/ready` (Qdrant, Blob y Table)
- [x] Pruebas de integración contra Azurite y Qdrant locales (se omiten si no están disponibles)
- [x] `compose.yaml` con Qdrant, Azurite y el backend Go (perfil `app`), con health checks y red interna
- [x] `deploy/docker/api.Dockerfile` (compilación estática) verificado con `docker compose build`

La fase 3 añade Service Bus, su emulador, los consumidores Go, el outbox y
los endpoints de ingesta. El worker Python corresponde a la fase 4.

## Fase 3 — Ingesta y procesamiento asíncrono

Estado: **completada**. El flujo local usa Azurite y el emulador de Azure
Service Bus. El worker completa esta fase después de verificar el original
y asignar su tipo de pipeline. El procesamiento documental empieza en la
fase 4.

- [x] `POST /v1/resources`: subida multipart con validación e idempotencia
- [x] Persistencia de originales en Blob y consulta con `GET /v1/resources/{id}`
- [x] Creación de trabajos con transacción de Azure Table y encolado fiable mediante outbox
- [x] Integración con Azure Service Bus y configuración del emulador local
- [x] Consumidores Go idempotentes (`cmd/worker`)
- [x] Reintentos con backoff, DLQ y reconciliación tras interrupciones
- [x] `GET /v1/jobs/{id}` y `POST /v1/jobs/{id}/cancel`
- [x] Esquemas JSON versionados de ingesta y del futuro worker Python (`contracts/events`)

## Fase 4 — Procesamiento documental e IA

Estado: **completada**. Validada en una pila Docker aislada con Docling,
Tesseract, BGE-M3 en CPU, Azurite, Service Bus y Qdrant. Se probaron PDF
nativos y escaneados, Excel, Word, PowerPoint, búsqueda con referencias y
recuperación ante indisponibilidad del extractor, del índice y del modelo.

El arranque Docker de la fase 4 detecta las GPU disponibles. Prueba NVIDIA
con TEI, AMD con Infinity y ROCm, y usa CPU si no consigue arrancar el
modelo en GPU. Los comandos están al final del [README](../README.md#phase-4-startup-commands).

- [x] Worker Python de documentos (`workers/media`) e integración de Docling y Tesseract
- [x] Fragmentación con referencias por formato y checkpoints duraderos
- [x] BGE-M3 en salida densa e indexado idempotente en `text_embeddings`
- [x] `POST /v1/search` con aislamiento por tenant
- [x] Reindexado de originales existentes y recuperación entre etapas

## Fase 5 — Agentic RAG

- [ ] Adaptador de Qwen3
- [ ] Orquestador del agente con límites (iteraciones, llamadas, tokens, tiempo)
- [ ] Registro de herramientas autorizadas (8 herramientas iniciales)
- [ ] Adaptador de BGE Reranker v2 M3
- [ ] Respuestas con referencias a fuentes
- [ ] `POST /v1/queries`

## Fase 6 — Procesamiento multimodal

- [ ] Integración de FFmpeg y OpenCV
- [ ] Integración de faster-whisper
- [ ] Adaptador de Qwen3-VL
- [ ] Adaptador de Cosmos-Embed1 (NVIDIA NIM)
- [ ] Búsqueda multimodal con fusión RRF

## Fase 7 — Evolución hacia microservicios

- [ ] Evaluar necesidades reales de escalabilidad
- [ ] Identificar módulos candidatos a extracción
- [ ] Sustituir interfaces internas por adaptadores remotos
- [ ] Separar propiedad de los datos
- [ ] Incorporar API Gateway cuando esté justificado

---

## Decisiones tomadas

| # | Decisión | ADR |
|---|---|---|
| 1 | Monolito modular con arquitectura hexagonal y DDD pragmático | `0001` |
| 2 | Azure Table Storage como fuente de verdad del estado de trabajos | `0002` |
| 3 | Motores de inferencia estándar por modelo, tras interfaces Go | `0003` |
| 4 | Una colección Qdrant por modelo y fusión de rankings por RRF | `0004` |
| 5 | Monorepo con el módulo Go en la raíz | `0005` |
| 6 | Publicación fiable mediante patrón outbox | `0006` |

## Bloqueos

Ninguno que impida avanzar. No se dispone todavía de cuenta ni suscripción de
Microsoft Azure, lo que **no bloquea** el desarrollo: Blob y Table Storage se
prueban contra el emulador Azurite y Qdrant en contenedor local. La migración a
Azure real será un cambio de configuración y credenciales, no de código.

## Pendientes de confirmación (no bloqueantes)

- Versiones concretas de pesos y motores de IA, y sus licencias.
- Dimensiones de los vectores (BGE-M3 confirmada en 1024; Cosmos-Embed1 pendiente).
- Salidas reales de BGE-M3 según motor (denso/disperso/multivector).
- Dimensionamiento de GPU y elección entre Container Apps GPU, VM GPU y AKS.
- Mecanismos de autenticación y autorización corporativos (ENS Alta).
- Alcance real del emulador local de Azure Service Bus.
