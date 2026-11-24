# ADR 0004 — Colecciones Qdrant y fusión de rankings

- **Estado:** Aceptada (dimensiones y salidas por confirmar al desplegar)
- **Fecha:** 2026-11-24
- **Contexto de la decisión:** diseño del almacenamiento vectorial

## Contexto

MAR indexa contenido heterogéneo generado por **modelos distintos**: embeddings textuales (BGE-M3) y embeddings visuales (Cosmos-Embed1). Además, muchos modelos de embedding producen **más de una representación** del mismo contenido (densa, dispersa, multivector).

Comparar vectores de espacios distintos produce puntuaciones sin significado, y mezclar métricas de similitud heterogéneas invalida cualquier umbral.

## Decisión

1. **Una colección por modelo:**

| Colección | Modelo | Contenido |
|---|---|---|
| `text_embeddings` | BGE-M3 | Documentos, transcripciones y descripciones visuales de Qwen3-VL |
| `visual_embeddings` | Cosmos-Embed1 | Escenas y fragmentos visuales de vídeo |

2. **Vectores nombrados** dentro de la colección para las distintas salidas del mismo modelo (`dense`, `sparse`, `colbert`), **cuando el motor desplegado las exponga**. No se asume que vLLM y FlagEmbedding ofrezcan las mismas salidas.
3. **Prohibido comparar vectores de modelos distintos.**
4. **Fusión de rankings con RRF (Reciprocal Rank Fusion)** para combinar resultados de espacios vectoriales diferentes o de estrategias de recuperación distintas: se fusionan **posiciones**, no puntuaciones.
5. **El reranker textual solo se aplica a contenido textual**, nunca a vectores visuales directamente.
6. Las **descripciones visuales de Qwen3-VL se indexan como texto** con BGE-M3, manteniendo el vínculo con el frame de origen.
7. `point_id` **determinista** a partir de `resource_id` + `fragment_id` + `model_id` ⇒ upsert idempotente ante reentregas.
8. Payload obligatorio por punto (trazabilidad completa) e **índices de payload** sobre `tenant_id`, `resource_id` y `content_type`.

## Consecuencias

**Positivas**

- Recuperación correcta: cada espacio se consulta con su propio modelo y métrica.
- Idempotencia real en la indexación: los reintentos no duplican puntos.
- Trazabilidad de extremo a extremo dentro del propio índice.
- RRF evita inventar pesos entre puntuaciones incomparables.

**Negativas / costes**

- Dos consultas y una fusión por consulta multimodal.
- La configuración de la colección depende del motor de embeddings desplegado.
- Cambiar de modelo de embedding obliga a **reindexar** en una colección nueva (no se puede reutilizar la existente).

**Mitigación**

- Parametrizar el nombre y la dimensión de la colección por configuración, y **validar al arranque** que la colección existente coincide con la configuración esperada.

## Pendiente

- Confirmar la **dimensión** de los vectores de Cosmos-Embed1 por variante.
- Confirmar qué salidas de BGE-M3 expone el motor elegido.
