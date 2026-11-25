# ADR 0003 — Modelos de IA y motores de inferencia

- **Estado:** Aceptada (versiones concretas pendientes)
- **Fecha:** 2026-11-24
- **Contexto de la decisión:** diseño del plano de inferencia

## Contexto

MAR requiere exactamente seis modelos, sin sustitución sin autorización:

| Modelo | Responsabilidad |
|---|---|
| Cosmos-Embed1 | Embeddings visuales de vídeo y búsqueda de escenas |
| Faster-Whisper | Transcripción de audio con timestamps |
| BGE-M3 | Embeddings textuales |
| Qwen3-VL | Comprensión visual |
| Qwen3 | Razonamiento, Agentic RAG y respuestas |
| BGE Reranker v2 M3 | Reordenación de resultados |

Restricciones:

- Los modelos deben permanecer **desacoplados de la lógica de negocio**.
- **No se desarrollan servidores Python personalizados** cuando exista un servidor de inferencia compatible.
- Debe ser posible **sustituir un modelo sin tocar la lógica de negocio**.

## Decisión

Servir cada modelo con un **motor de inferencia estándar** y acceder a él desde Go mediante **adaptadores tras interfaces**:

| Modelo | Motor | Justificación |
|---|---|---|
| Cosmos-Embed1 | NVIDIA NIM | Microservicio oficial del modelo; API REST compatible con Embeddings de OpenAI |
| Faster-Whisper | Python + CTranslate2 | Motor nativo del modelo. **La librería no expone API REST**, por lo que se integra como worker/servidor de transcripción (excepción explícita y acotada a la regla de no crear servidores propios) |
| BGE-M3 | Text Embeddings Inference en NVIDIA y CPU. Infinity con PyTorch ROCm en AMD. vLLM o FlagEmbedding según el despliegue | Los motores locales sirven embeddings densos mediante `/v1/embeddings`. vLLM y FlagEmbedding pueden exponer salidas adicionales según la configuración. |
| Qwen3-VL | vLLM | Soportado oficialmente |
| Qwen3 | vLLM | Soportado oficialmente; variantes MoE y FP8 validadas |
| BGE Reranker v2 M3 | FlagEmbedding o vLLM | FlagEmbedding (`FlagReranker`) o vLLM con tarea `score` |

Interfaces Go que aíslan el motor concreto: `Embedder`, `Transcriber`, `VisionModel`, `ChatModel`, `Reranker`.

Los endpoints se configuran por **nombre lógico** (`MAR_MODEL_QWEN3_URL`), no por motor.

## Consecuencias

**Positivas**

- Un único patrón operativo para la mayoría de los modelos (vLLM), con menos superficie de mantenimiento.
- Cambiar de motor o de versión no afecta al dominio ni a los casos de uso.
- Los mocks de desarrollo implementan las mismas interfaces: se puede desarrollar sin GPU.

**Negativas / costes**

- Las **capacidades no son equivalentes entre motores**: vLLM expone recuperación densa de BGE-M3, mientras que FlagEmbedding añade disperso y multivector. La configuración de la colección depende del motor elegido.
- Cada motor tiene su propio formato de entrada/salida pese a la compatibilidad con la API de OpenAI.
- Requiere verificar licencias y requisitos de hardware por revisión de modelo.

**Mitigación**

- Los adaptadores normalizan el formato hacia el contrato interno.
- La disponibilidad real de salidas de BGE-M3 se determina al desplegar (ver ADR 0004).

## Pendiente

La fase 4 fija BGE-M3 en la revisión `5617a9f61b028005a4858fdac845db406aefb181` para la búsqueda densa local. El arranque prueba TEI 1.9 en NVIDIA, Infinity en AMD y TEI en CPU según los dispositivos disponibles. Queda por fijar el despliegue de producción y las revisiones de los modelos de fases posteriores en `docs/ROADMAP.md`.
