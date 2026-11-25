# MAR Project

## Overview

MAR is an AI-powered platform designed to ingest, process, and analyze
multimodal data, including documents, images, and videos.

It combines asynchronous processing, vector databases, and Agentic RAG
to enable intelligent information retrieval, automated analysis, and
context-aware responses through a scalable and modular architecture.

## Development Status

**Last updated:** November 25, 2026

| Phase | Description | Status |
|:---:|---|---|
| 1 | Core Infrastructure | ✅ Completed |
| 2 | Persistence and Storage | ✅ Completed |
| 3 | Data Ingestion and Asynchronous Processing | ✅ Completed |
| 4 | Document Processing and AI | 🔵 Next |
| 5 | Agentic RAG | ⏳ Pending |
| 6 | Multimodal Processing | ⏳ Pending |
| 7 | Evolution Towards Microservices | ⏳ Pending |

## Local development

Run these commands from the repository root. The Docker-based setup requires Docker with
the Compose plugin. The local defaults use Azurite. You can run the API without
an Azure account or an `.env` file. Enable the Service Bus emulator and the
Go worker to try the asynchronous ingestion flow.

### Run everything in Docker

```bash
docker compose --profile app up -d --build
docker compose --profile app ps
```

This builds the Go API and starts it alongside Qdrant and Azurite. If you have
Make installed, `make up-full` does the same. The API is available at
`http://localhost:8080`. Set `MAR_DOCKER_HTTP_PORT=18080` if the default port
is occupied.

```bash
docker compose --profile app logs -f api  # Follow API logs
docker compose --profile app stop       # Stop the stack and keep its containers
```

### Run the Go API on your machine

This option requires Make and the Go version specified in [go.mod](go.mod).

```bash
make up   # Start Qdrant and Azurite in Docker
make ps   # Wait until both services report healthy before running the API
make run  # Start the Go API in the foreground
```

`make run` loads an `.env` file if present. Press `Ctrl+C` to stop the API.
If port `8080` is already in use, run
`MAR_HTTP_ADDR=127.0.0.1:18080 make run` and use port `18080` in the checks
below. If Qdrant or Azurite is already running on the default ports, reuse
the existing service. For example, with Qdrant already running, start only
Azurite with `docker compose up -d azurite` before running the API.

| Service | Host ports |
|---|---|
| MAR API | `8080` (HTTP) |
| Qdrant | `6333` (REST and dashboard), `6334` (gRPC) |
| Azurite | `10000` (Blob), `10001` (Queue), `10002` (Table) |

Use `docker compose stop` to stop just the infrastructure. `docker compose
down` removes the containers. Qdrant data is kept in a named volume, while
Azurite has no persistent volume in this setup.

### Verify the API and storage

With the API running, open a separate terminal and run:

```bash
curl -i http://localhost:8080/health/live
curl -i http://localhost:8080/health/ready
curl -i http://localhost:8080/v1/jobs/demo
curl -i -H 'X-Tenant-ID: demo' http://localhost:8080/v1/jobs/demo
curl -fsS http://localhost:6333/collections/text_embeddings
```

| Check | Expected result |
|---|---|
| `/health/live` | `200` with `{"status":"ok"}` when the API is running. |
| `/health/ready` | `200` with `blob`, `table`, and `qdrant` in `ok`. If a dependency check fails, the API returns `503`. |
| `/v1/jobs/demo` without `X-Tenant-ID` | `401` with the default configuration. |
| `/v1/jobs/demo` with `X-Tenant-ID` | `400` because `demo` is not a UUID. Use a real job ID to get `200`. |
| Qdrant collection `text_embeddings` | A `dense` vector with 1024 dimensions and cosine distance. |

The API also creates the `raw`, `derived`, `frames`, and `transcripts` Blob
containers and the `resources` and `jobs` tables on startup. The warning about
skipping `visual_embeddings` is expected when `MAR_QDRANT_VISUAL_VECTOR_SIZE`
is unset.

### Run asynchronous ingestion

For the complete local ingestion flow, create a local `.env` with the
settings below. Set `MAR_EMULATOR_ACCEPT_EULA=Y` after reviewing the
[emulator and SQL Server license terms](https://learn.microsoft.com/en-us/azure/service-bus-messaging/test-locally-with-service-bus-emulator).
Use your own strong password for the emulator's SQL Server.

```dotenv
MAR_EMULATOR_ACCEPT_EULA=Y
MAR_EMULATOR_SQL_PASSWORD='replace-with-a-strong-local-password'
```

Start the API, the Go worker, and all dependencies:

```bash
make up-ingestion
make ps
curl -fsS http://localhost:5300/health
```

Service Bus uses port `5672` for AMQP and `5300` for emulator health. The
queue `ingestion` is preconfigured locally. `make up-ingestion` starts the
Compose Qdrant on ports `6333` and `6334`, which must be free.
Run `docker compose --profile app --profile messaging stop` to stop the
stack and keep its containers.

To run the API and worker on the host instead, start only the dependencies
with `docker compose up -d qdrant azurite servicebus`. Add these settings to
your local `.env`, then run `make run` and `make run-worker` in separate
terminals:

```dotenv
MAR_SERVICEBUS_ENABLED=true
MAR_SERVICEBUS_CONNECTION_STRING='Endpoint=sb://127.0.0.1;SharedAccessKeyName=RootManageSharedAccessKey;SharedAccessKey=SAS_KEY_VALUE;UseDevelopmentEmulator=true'
```

For a provisioned Azure Service Bus namespace, set
`MAR_SERVICEBUS_CONNECTION_STRING` to its connection string and
`MAR_SERVICEBUS_QUEUE_INGESTION` to the queue name if it differs from
`ingestion`. Configure the queue with a peek-lock duration of at least one
minute. The API can accept uploads and persist the outbox with Service Bus
disabled. Pending jobs are published once a relay is started with Service Bus
enabled.

#### Upload and inspect a job

Create a file with `printf 'MAR ingestion test\n' > example.txt` and run:

```bash
curl -i -H 'X-Tenant-ID: demo' -H 'Idempotency-Key: first-upload' \
  -F 'file=@example.txt' http://localhost:8080/v1/resources
```

The response is `202 Accepted` with `resource_id`, `job_id`, and `status`.
Copy the IDs to inspect the metadata and job status:

```bash
resource_id='replace-with-resource-id'
job_id='replace-with-job-id'
curl -i -H 'X-Tenant-ID: demo' "http://localhost:8080/v1/resources/$resource_id"
curl -i -H 'X-Tenant-ID: demo' "http://localhost:8080/v1/jobs/$job_id"
curl -i -X POST -H 'X-Tenant-ID: demo' "http://localhost:8080/v1/jobs/$job_id/cancel"
```

Cancellation returns `409` if the job has already completed or failed. To try cancellation
before processing, stop the worker, upload a new file, cancel its job, then
start the worker again. Use a new idempotency key for each new upload. Reusing
a key in the same tenant with the same file returns the same IDs. Reusing it
with a different file returns `409`.

The default request limit is 100 MiB including multipart overhead
(`MAR_HTTP_MAX_BODY_BYTES`). In Phase 3 the worker verifies the original's
size and SHA-256, classifies it, and sets the job to `COMPLETED`. The
resource stays `STORED` until a later phase processes and indexes its
contents. See the [HTTP contract](contracts/openapi/openapi.yaml) and the
[event contracts](contracts/events).

### Run the tests

With Go and Make installed, start Qdrant and Azurite before running the tests.
The storage tests connect directly to those services.

```bash
make build
make vet
make test
go test -count=1 -v ./internal/platform/httpapi ./internal/storage/...
make race
```

Check the verbose output for `SKIP` to confirm that the ingestion and storage
integration tests ran. They skip when their dependencies are unreachable.
