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
the Compose network. Use the API port from your `.env` (port `18082` in the
current local setup). See the [startup commands](#phase-4-startup-commands)
at the end of this README.

#### Upload, check the job and search

Use a PDF with text, a scanned PDF, or an Excel workbook. The same upload
also supports DOCX, PPTX, plain UTF-8 text, Markdown and comma-separated CSV.
Use a different idempotency key for a separate upload. The extractor accepts
up to 200 PDF pages, 100,000 XLSX cells and 3,000 fragments per document.

```bash
BASE=http://localhost:18082
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

query=$(jq -nc --arg id "$RESOURCE_ID" \
  '{query:"¿Cuánto me pagan por comer durante un viaje?",limit:5,resource_id:$id}')
curl -fsS -H "X-Tenant-ID: $TENANT" -H 'Content-Type: application/json' \
  -d "$query" "$BASE/v1/search" | jq .
```

Run the job query again until it reaches `COMPLETED` with
`stage: document_indexed` and `progress: 100`. The resource should then be
`PROCESSED`. Change `query` to match your document. Search returns passages
and their source references. A PDF hit can include `page`, `pages` for a multi-page block, and `section`,
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

See the [failure drills](#phase-4-failure-drills) at the end of this README.

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

Run `python3 -m unittest discover -s workers/media` for the Python fragment
and provenance tests.

Check the verbose output for `SKIP` to confirm that the ingestion and storage
integration tests ran. They skip when their dependencies are unreachable.

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

Check the API and all configured storage dependencies:

```bash
BASE=http://localhost:18082
curl -fsS "$BASE/health/ready"
```

### Stop all MAR services

From the repository root, stop the API, Go worker, document extractor,
BGE-M3, Qdrant, Azurite, Service Bus and its SQL Server with:

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
BASE=http://localhost:18082
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

On a local x86-64 machine, run these commands from the repository root:

```bash
export DOCKER_CONTEXT=default
make up-queries
make queries-ps
```

`make up-queries` starts Qwen3-1.7B and BGE Reranker v2 M3 using vLLM. It
prefers a compatible NVIDIA or AMD GPU with enough available memory and keeps
CPU servers ready as a fallback. Set `MAR_RAG_ACCELERATOR=cpu` to run Qwen3
and the reranker on CPU, or `MAR_RAG_ACCELERATOR=gpu` to require both to start
on GPU. BGE-M3 uses the accelerator chosen by the phase 4 startup script.
The first startup downloads model weights. The model servers are reachable
only on the Compose network.

In Postman, send `POST http://localhost:18082/v1/queries` with the same
`X-Tenant-ID` used to upload the PDF and select **Body → raw → JSON**.
Replace the example UUID with the real `resource_id`:

```json
{
  "query": "¿Qué pasa si borro los originales y luego quiero rehacer el índice?",
  "resource_id": "1bdd98eb-fe02-5078-87d6-a32d52a2d44d"
}
```

The response includes `status`, `answer` and `sources`. For the MAR
architecture proposal, check that the answer explains that the original
files must be requested again, and confirm the citation in the PDF. Ask
how much the deployment costs per month to check the
`insufficient_evidence` response with an empty source list.
Omit `resource_id` to query across the tenant's indexed documents.

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
