# MAR Project

## Overview

MAR is an AI-powered platform designed to ingest, process, and analyze
multimodal data, including documents, images, and videos.

It combines asynchronous processing, vector databases, and Agentic RAG
to enable intelligent information retrieval, automated analysis, and
context-aware responses through a scalable and modular architecture.

## Development Status

**Last updated:** November 27, 2026

| Phase | Description | Status |
|:---:|---|---|
| 1 | Core Infrastructure | ✅ Completed |
| 2 | Persistence and Storage | ✅ Completed |
| 3 | Data Ingestion and Asynchronous Processing | ✅ Completed |
| 4 | Document Processing and AI | ✅ Completed |
| 5 | Agentic RAG for documents | ✅ Completed |
| 6 | Multimodal Processing | ⏳ Pending |
| 7 | Evolution Towards Microservices | ⏳ Pending |

The document workflow runs from asynchronous upload and indexing to semantic
search and answers with source citations. Queries return `insufficient_evidence`
when indexed passages do not support an answer. See [Phase 5 document
queries](#phase-5-document-queries) to try it locally.

## Local development

Run these commands in Bash from the root of the `dev` checkout. Docker builds
and runs the Go API and worker, Docling and Tesseract, Qdrant, the Azure
emulators and SQL Server, and the model servers. The Docker setup uses local
emulators, so it needs no Azure account or subscription.

### Prerequisites

- A running Docker engine and the Docker Compose plugin with `up --wait`
  support. Run `docker info` and `docker compose version` to verify access.
  If you use more than one Docker engine, check `docker context ls` and use
  the same context for startup, logs and shutdown. On a Linux machine using
  Docker Engine for GPU access, select that engine with
  `export DOCKER_CONTEXT=default` if needed.
- Bash and GNU Make for the `make` commands. Install `curl` and `jq` to run
  the API examples. Go and Python are only needed on the host for the
  host-development and test commands below.
- Use a Linux x86-64 Docker engine for the full stack. The default vLLM CPU
  image targets x86-64 and the Service Bus emulator uses SQL Server. Even
  with GPU acceleration, the query stack starts CPU model servers as a
  fallback, so allocate memory for them as well. [vLLM CPU requirements](https://docs.vllm.ai/en/v0.17.1/getting_started/installation/cpu/) ·
  [SQL Server Docker requirements](https://learn.microsoft.com/en-us/sql/linux/quickstart-install-connect-docker?view=sql-server-ver17)
- Allow network access for the first run to download container images, build
  dependencies and Hugging Face model weights. Leave room in Docker storage
  for the images and model caches. The first build and startup can take
  several minutes. GPU images increase disk usage, especially on AMD.

### Quick start for document queries

Create or edit `.env` in the repository root. After reviewing the
[Service Bus emulator and SQL Server license terms](https://learn.microsoft.com/en-us/azure/service-bus-messaging/test-locally-with-service-bus-emulator),
set `MAR_EMULATOR_ACCEPT_EULA=Y` and replace the password placeholder with a
local password. SQL Server requires at least eight characters and characters
from at least three categories among uppercase letters, lowercase letters,
digits and symbols. [SQL Server password policy](https://learn.microsoft.com/en-us/sql/linux/quickstart-install-connect-docker?view=sql-server-ver17)

```dotenv
MAR_EMULATOR_ACCEPT_EULA=Y
MAR_EMULATOR_SQL_PASSWORD='replace-with-your-local-password'
MAR_DOCKER_HTTP_PORT=8080
```

The following commands start the full stack and verify the API and message broker.

```bash
make up-queries
make queries-ps
BASE=http://localhost:8080
curl -fsS "$BASE/health/ready" | jq .
curl -fsS --retry 30 --retry-connrefused --retry-delay 2 http://localhost:5300/health
```

Use the port from `MAR_DOCKER_HTTP_PORT` in `BASE` if you changed it. The
API readiness endpoint checks Blob, Table and Qdrant. Use `make queries-ps`
to check the model containers and the separate `/health` call on port `5300`
to check the Service Bus emulator. The emulator can return `503` while its
SQL Server initializes. The command retries during that window. See the
[host ports](#host-ports) below if Docker reports an address already in use.

`make up-queries` selects a compatible GPU automatically when available. To
use CPU for every model, run `MAR_DOCUMENT_ACCELERATOR=cpu MAR_RAG_ACCELERATOR=cpu make up-queries`.
For a slow first model download, increase the per-service wait, for example
with `MAR_DOCUMENT_START_TIMEOUT=1200 make up-queries`.

#### Try an upload and a question

Keep the same Bash terminal so `BASE`, `TENANT` and the returned IDs are
available. The following commands create and upload a small text document.

```bash
TENANT=mar-demo
DEMO_DIR=$(mktemp -d)
printf 'The daily travel meal allowance is 55 euros.\n' > "$DEMO_DIR/allowance.txt"

submission=$(curl -fsS -H "X-Tenant-ID: $TENANT" \
  -F "file=@$DEMO_DIR/allowance.txt" "$BASE/v1/resources")
printf '%s\n' "$submission" | jq .
JOB_ID=$(jq -er .job_id <<<"$submission")
RESOURCE_ID=$(jq -er .resource_id <<<"$submission")
```

Use the following request to check the job. Repeat it until `status` is
`COMPLETED` and `stage` is `document_indexed`. If it reaches `FAILED`, inspect
`error_code` and `error_message` before continuing.

```bash
curl -fsS -H "X-Tenant-ID: $TENANT" "$BASE/v1/jobs/$JOB_ID" | jq .
```

This request asks a question scoped to that document.

```bash
question=$(jq -nc --arg id "$RESOURCE_ID" \
  '{query:"What is the daily travel meal allowance?",resource_id:$id}')
curl -fsS -H "X-Tenant-ID: $TENANT" -H 'Content-Type: application/json' \
  -d "$question" "$BASE/v1/queries" | jq .
```

Look for `status: answered`, the amount and a citation in `sources`. In
Postman, use the same URL and `X-Tenant-ID` header and paste the JSON printed
by `printf '%s\n' "$question"` into **Body → raw → JSON**. Use
`make stop-queries` to stop the stack while keeping its data.

### Run only the API and storage in Docker

```bash
docker compose --profile app up -d --build
docker compose --profile app ps
```

This builds the Go API and starts it alongside Qdrant and Azurite. The same
setup is available through `make up-full` and needs no `.env` file. The API
is available at `http://localhost:8080`. Set `MAR_DOCKER_HTTP_PORT=18080` if
the default port is occupied.

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

### Host ports

| Service | Host ports |
|---|---|
| MAR API | `8080` (HTTP, configurable with `MAR_DOCKER_HTTP_PORT` for Docker or `MAR_HTTP_ADDR` for `make run`) |
| Qdrant | `6333` (REST and dashboard), `6334` (gRPC) |
| Azurite | `10000` (Blob), `10001` (Queue), `10002` (Table) |
| Service Bus emulator | `5672` (AMQP), `5300` (health), both bound to loopback |

The full stack starts its own Qdrant and Azurite containers. Their host ports
must be available. Docling, BGE-M3, vLLM and the emulator's SQL Server listen
on the Compose network.

Use `docker compose stop` to stop just the infrastructure. `docker compose
down` removes the containers. Qdrant data is kept in a named volume, while
Azurite has no persistent volume in this setup.

### Verify the API and storage

With the API running, open a separate terminal and use its configured port.

```bash
BASE=http://localhost:8080
curl -i "$BASE/health/live"
curl -i "$BASE/health/ready"
curl -i "$BASE/v1/jobs/demo"
curl -i -H 'X-Tenant-ID: demo' "$BASE/v1/jobs/demo"
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

Use the [quick start `.env` settings](#quick-start-for-document-queries).
The following commands start the API, the Go worker and the storage and
messaging dependencies for ingestion.

```bash
make up-ingestion
make ps
curl -fsS --retry 30 --retry-connrefused --retry-delay 2 http://localhost:5300/health
```

The queue `ingestion` is preconfigured locally.
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
BASE=http://localhost:8080
curl -i -H 'X-Tenant-ID: demo' -H 'Idempotency-Key: first-upload' \
  -F 'file=@example.txt' "$BASE/v1/resources"
```

The response is `202 Accepted` with `resource_id`, `job_id`, and `status`.
Copy the IDs to inspect the metadata and job status:

```bash
resource_id='replace-with-resource-id'
job_id='replace-with-job-id'
curl -i -H 'X-Tenant-ID: demo' "$BASE/v1/resources/$resource_id"
curl -i -H 'X-Tenant-ID: demo' "$BASE/v1/jobs/$job_id"
curl -i -X POST -H 'X-Tenant-ID: demo' "$BASE/v1/jobs/$job_id/cancel"
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

### Run document processing and search

Use the same local `.env` as for asynchronous ingestion. The first start
downloads the BGE-M3 weights and the first PDF warms up the Docling models.
Model caches use Docker volumes. Docling and Tesseract run on CPU. At
startup MAR checks which GPUs Docker can access. It tries NVIDIA with
Text Embeddings Inference (TEI), then AMD with Infinity and PyTorch ROCm.
If neither GPU starts a healthy model server, automatic mode starts TEI
on CPU. All three options use the same BGE-M3 revision, CLS pooling and
1024-dimensional dense vectors.

NVIDIA requires the NVIDIA driver and NVIDIA Container Toolkit. AMD uses
Linux x86-64 with `amdgpu`, compatible ROCm hardware and access to
`/dev/kfd` and the GPU's render device from Docker. The first AMD start
builds an image for the detected GPU architecture and downloads its own
copy of the model weights. Later starts reuse the image layers and weights.
[NVIDIA Container Toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/sample-workload.html)
· [AMD ROCm PyTorch installation](https://rocm.docs.amd.com/projects/ai-ecosystem/en/latest/frameworks/pytorch/install.html)

`make up-documents` waits for the extractor and BGE-M3 to be healthy before
bringing up the full stack. The extraction and embedding services listen on
the Compose network. See the [startup commands](#phase-4-startup-commands)
for status and logs.

#### Upload, check the job and search

Use a PDF with text, a scanned PDF, or an Excel workbook. The same upload
also supports DOCX, PPTX, plain UTF-8 text, Markdown and comma-separated CSV.
Use a different idempotency key for a separate upload. The extractor accepts
up to 200 PDF pages, 100,000 XLSX cells and 3,000 fragments per document.

```bash
BASE=http://localhost:8080
FILE=/path/to/document.pdf
TENANT=demo-fase4

submission=$(curl -fsS -H "X-Tenant-ID: $TENANT" \
  -H "Idempotency-Key: phase4-$(date +%s)" \
  -F "file=@$FILE" "$BASE/v1/resources")
printf '%s\n' "$submission" | jq .
JOB_ID=$(jq -r .job_id <<<"$submission")
RESOURCE_ID=$(jq -r .resource_id <<<"$submission")

curl -fsS -H "X-Tenant-ID: $TENANT" "$BASE/v1/jobs/$JOB_ID" | jq .
curl -fsS -H "X-Tenant-ID: $TENANT" "$BASE/v1/resources/$RESOURCE_ID" | jq .
```

Repeat the job request until it reaches `COMPLETED` with
`stage: document_indexed` and `progress: 100`. The resource should then be
`PROCESSED`. Set `query` to a question relevant to your document.

```bash
query=$(jq -nc --arg id "$RESOURCE_ID" \
  '{query:"¿Cuánto me pagan por comer durante un viaje?",limit:5,resource_id:$id}')
curl -fsS -H "X-Tenant-ID: $TENANT" -H 'Content-Type: application/json' \
  -d "$query" "$BASE/v1/search" | jq .
```

Search returns passages and their source references. A PDF hit can include
`page`, `pages` for a multi-page block, and `section`,
while an Excel hit includes `sheet` and `cells`. The optional `resource_id`
narrows the search to one document. Using a different `X-Tenant-ID` returns
no results for that document.

To process an existing Phase 3 upload or resume a job after exhausting its
automatic retries, enqueue the stored resource with a **new** idempotency key:

```bash
curl -fsS -X POST -H "X-Tenant-ID: $TENANT" \
  -H "Idempotency-Key: reindex-$(date +%s)" \
  "$BASE/v1/resources/$RESOURCE_ID/index" | jq .
```

Use the new `job_id` when polling. A repeat with the same key returns the
same job. A new job for the same original can reuse the stored extraction
and embedding batches.

See the [failure drills](#phase-4-failure-drills) to test retries and recovery.

### Run the tests

With Go and Make installed, run `make up` and wait for Qdrant and Azurite to
be healthy. The storage tests connect directly to those services. `make race`
also needs a C compiler on the host.

```bash
make build
make vet
make test
go test -count=1 -v ./internal/platform/httpapi ./internal/storage/...
make race
```

Use Python 3.12, as in the worker image, to run
`python3 -m unittest discover -s workers/media` for the fragment and provenance tests.

Check the verbose output for `SKIP`. It marks the ingestion and storage
integration tests that could not reach their dependencies.

## Phase 4 startup commands

Run these from the repository root with the local `.env` configured for
Azurite and the Service Bus emulator.

| Command | What starts or changes |
|---|---|
| `make up-documents` | Starts the API, Go worker, Qdrant, Azurite, Service Bus, Docling and BGE-M3. Tries NVIDIA, then AMD, then CPU as available. |
| `make up-document-models` | Starts Docling and BGE-M3 only. Uses the same automatic accelerator selection. |
| `MAR_DOCUMENT_ACCELERATOR=cpu make up-documents` | Starts the full stack with BGE-M3 on CPU. |
| `MAR_DOCUMENT_ACCELERATOR=gpu make up-documents` | Requires BGE-M3 to start on a supported NVIDIA or AMD GPU. Reports an error if neither works. |
| `make documents-ps` | Shows the running containers, the inference image and each reported health status. |
| `make documents-logs` | Follows the API, Go worker, extractor and model logs. |
| `make stop-documents` | Stops the stack and keeps the containers and their data. |

The same `MAR_DOCUMENT_ACCELERATOR` override works with
`make up-document-models`. Set `MAR_DOCUMENT_START_TIMEOUT` in seconds if
the first model download needs more than the default 600 seconds.
On AMD, `make documents-ps` shows an image such as
`mar-backend/embeddings-rocm:gfx1200`. The `embeddings` log shows the GPU name
and ROCm version after a successful computation on the device.

To start the API and Go worker separately after starting the model services,
use the following command. Compose also starts their storage and messaging
dependencies when needed.

```bash
docker compose -f compose.yaml -f compose.documents.yaml \
  --profile app --profile messaging --profile documents up -d --build api worker
```

Check the API and its storage dependencies.

```bash
BASE=http://localhost:8080
curl -fsS "$BASE/health/ready"
```

### Stop the document-processing stack

From the repository root, stop the API, Go worker, document extractor,
BGE-M3, Qdrant, Azurite, Service Bus and its SQL Server with the commands below.
If you started Phase 5, use `make stop-queries` to include its model servers.

```bash
make stop-documents
make documents-ps
```

`make documents-ps` should show no running services after the stop. If you
started Go on the host with `make run` or `make run-worker`, press `Ctrl+C`
in each of those terminals as well. Run `make up-documents` when you want to
start MAR again.

The stop command keeps the containers and their data. Azurite stores its
data inside its container, so deleting that container also deletes the
local originals, derived files and tables.

### Phase 4 failure drills

Run these against your local development stack with `make up-documents`
already running. Prepare the commands below once from the repository root
in Bash. Each `submit` creates a new test resource so it starts with fresh
checkpoints.

```bash
BASE=http://localhost:8080
TENANT=demo-fallos
phase4=(docker compose -f compose.yaml -f compose.documents.yaml \
  --profile app --profile messaging --profile documents)

printf 'El importe de la dieta de viaje es de 55 euros.\n' >/tmp/mar-retry.txt
printf '%%PDF-1.7\ninvalid PDF content\n' >/tmp/mar-invalid.pdf

submit() {
  local response
  response=$(curl -fsS -H "X-Tenant-ID: $TENANT" \
    -F "file=@$1" "$BASE/v1/resources") || return
  JOB_ID=$(jq -er .job_id <<<"$response") || return
  RESOURCE_ID=$(jq -er .resource_id <<<"$response") || return
  jq . <<<"$response"
}

job() {
  curl -fsS -H "X-Tenant-ID: $TENANT" "$BASE/v1/jobs/$JOB_ID" | \
    jq '{status, stage, attempts, progress, error_stage, error_code}'
}
```

Run one scenario at a time. Call `job` repeatedly to see the intermediate
states. To catch a retry, call it once a second. Press `Ctrl+C` to stop
watching.

```bash
while true; do job; sleep 1; done
```

| Scenario | What to do | Expected result |
|---|---|---|
| **Extractor offline** | Run `"${phase4[@]}" stop media`, then `submit /tmp/mar-retry.txt`. When `job` shows the error, run `"${phase4[@]}" start media`. | `extracting` reports `extraction_unavailable`. A later attempt can complete as soon as the extractor is back. |
| **Qdrant offline** | Run `"${phase4[@]}" stop qdrant`, then `submit /tmp/mar-retry.txt`. When `job` shows the error, run `"${phase4[@]}" start qdrant`. | `indexing` reports `index_unavailable`. Qdrant calls may take a few seconds to time out. The retry uses the saved vectors and repeats the upserts with the same point IDs. |
| **BGE-M3 offline** | Run `"${phase4[@]}" stop embeddings`, then `submit /tmp/mar-retry.txt`. Watch the job reach `FAILED`, then run `"${phase4[@]}" start embeddings` and wait for it to become healthy. | `embedding` reports `embedding_unavailable`. The job reaches `FAILED` at attempt 3. Search returns HTTP `503` while the model is offline. |
| **Invalid PDF** | Run `submit /tmp/mar-invalid.pdf`. | `FAILED` at attempt 1 with `error_stage: extracting` and `error_code: document_conversion_failed`. |
| **Service Bus offline** | Run `"${phase4[@]}" stop servicebus`, then `submit /tmp/mar-retry.txt`. Check the job before running `"${phase4[@]}" start servicebus`. | The job stays `QUEUED` with zero attempts while the message is pending in the outbox. It can start processing when the emulator is ready again. |
| **Cancellation before processing** | Run `"${phase4[@]}" stop worker`, then `submit /tmp/mar-retry.txt`. Cancel with the command below and run `"${phase4[@]}" start worker`. | The job stays `CANCELLED`, with zero attempts when no worker had claimed it. |

```bash
curl -fsS -X POST -H "X-Tenant-ID: $TENANT" \
  "$BASE/v1/jobs/$JOB_ID/cancel" | jq '{status, attempts}'
```

For **worker interruption during processing**, pause the extractor, submit a
fresh document and run `job` until it shows `PROCESSING` and `attempts: 1`.
Then stop the worker, unpause the extractor and start the worker again.

```bash
"${phase4[@]}" pause media
submit /tmp/mar-retry.txt
job
# Once the job shows PROCESSING:
"${phase4[@]}" stop -t 5 worker
"${phase4[@]}" unpause media
"${phase4[@]}" start worker
```

The relay waits for the worker's lease to expire, then records
`worker_lease_expired` and schedules another attempt. Poll `job` to see the
same job reach `COMPLETED` on attempt 2.

A transient failure can schedule up to two retries, **5 seconds** after the
first failure and **10 seconds** after the second. During the wait the job is
`PROCESSING` with `stage: retry_scheduled`. If the third attempt fails, fix the
dependency and enqueue a new job for the same resource. The original and its
saved checkpoints remain available.

```bash
response=$(curl -fsS -X POST -H "X-Tenant-ID: $TENANT" \
  "$BASE/v1/resources/$RESOURCE_ID/index")
JOB_ID=$(jq -er .job_id <<<"$response")
job
```

Use a fixed `Idempotency-Key` if you want to repeat this enqueue request and
recover the same new job ID. For another processing attempt, send a new key
or omit the header.

## Phase 5 document queries

`POST /v1/queries` reuses the documents already indexed by phase 4.
BGE Reranker v2 M3 reorders the retrieved passages. Qwen3 writes an answer
from those passages, and MAR resolves the cited sources to their document,
fragment, and page or sheet. The model can request another search or the full
text of a source within the original tenant and resource scope. Each
request allows up to two tool calls during evidence retrieval. MAR checks
each selected quotation against the indexed text and verifies whether the
quotations answer the exact question before asking Qwen3 to write a response.

Follow the [quick start](#quick-start-for-document-queries) to configure the
emulator and run an upload through the full query stack.

`make up-queries` starts Qwen3-1.7B and BGE Reranker v2 M3 using vLLM. It
prefers a compatible NVIDIA or AMD GPU with enough available memory and keeps
CPU servers ready as a fallback. Set `MAR_RAG_ACCELERATOR=cpu` to run Qwen3
and the reranker on CPU, or `MAR_RAG_ACCELERATOR=gpu` to require both to start
on GPU. BGE-M3 uses the accelerator chosen by the phase 4 startup script.
The first startup downloads model weights. The model servers are reachable
only on the Compose network.

The response includes `status`, `answer` and `sources`. Ask for a detail that
is absent from the document to check the `insufficient_evidence` response
with an empty source list. Omit `resource_id` to query across the tenant's
indexed documents.

Run `make queries-ps` to see model health and `make documents-logs` to see
API, worker, extractor and BGE-M3 logs. For the broker and the query models,
run the following command in the same Docker context as startup.

```bash
docker compose -f compose.yaml -f compose.documents.yaml -f compose.queries.yaml \
  -f compose.queries.gpu.yaml --profile app --profile messaging --profile documents \
  --profile queries --profile query-gpu logs --tail=100 \
  servicebus servicebus-db qwen3 reranker qwen3-gpu reranker-gpu
```

For an API running directly on the host or in a custom deployment, set
`MAR_MODEL_QWEN3_URL` and `MAR_MODEL_RERANKER_URL` to the base URLs of the
model servers. MAR appends `/v1/chat/completions` and `/rerank` respectively.
The chat server must support automatic function calling and JSON Schema output
for Qwen3. Set the matching `MAR_MODEL_QWEN3_NAME` and
`MAR_MODEL_RERANKER_NAME` when they differ from the defaults, and pass
`MAR_MODEL_QWEN3_API_KEY` and `MAR_MODEL_RERANKER_API_KEY` if the model
servers require bearer authentication. Allow enough time for the query
using `MAR_HTTP_REQUEST_TIMEOUT` and a longer `MAR_HTTP_WRITE_TIMEOUT`.
The local Compose overlay uses 90 and 100 seconds respectively.

Use `make stop-queries` to stop the full phase 5 stack and retain its data.
