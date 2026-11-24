# ADR 0002 — Azure Table Storage para estados de trabajos

- **Estado:** Aceptada (propuesta inicial, revisable)
- **Fecha:** 2026-11-24
- **Contexto de la decisión:** Fase 1/2 — gestión de trabajos

## Contexto

MAR necesita persistir el **estado operativo** de los trabajos de ingesta y procesamiento: estado, progreso, etapa, intentos, último error y marcas temporales. Los requisitos son:

- Escritura frecuente de actualizaciones pequeñas, con control de concurrencia.
- Consulta por identificador y listado por tenant.
- Coste bajo y operación mínima.
- Base para garantizar **publicación fiable** de mensajes (patrón outbox).

Restricciones explícitas del proyecto:

- **Service Bus no es fuente de verdad.**
- **Qdrant no es base de datos transaccional** y no se usa para gestionar trabajos.

## Decisión

Usar **Azure Table Storage** como almacén de estado operativo, con:

- `PartitionKey = tenant_id`, `RowKey = job_id`.
- Actualizaciones **condicionales por ETag** (concurrencia optimista).
- Entidades `job` y `outbox` en la **misma partición** para aprovechar el **lote transaccional dentro de una partición** (requisito del patrón outbox, ver ADR 0006).
- Estados: `PENDING`, `QUEUED`, `PROCESSING`, `COMPLETED`, `FAILED`, `CANCELLED`, con transiciones validadas en el dominio.

## Consecuencias

**Positivas**

- Coste muy bajo y escalado horizontal por partición.
- ETag nativo da control de concurrencia sin coordinación externa.
- El lote atómico intra-partición habilita el outbox sin necesidad de transacciones distribuidas.
- Azurite permite emularlo en desarrollo local sin depender de Azure.

**Negativas / costes**

- **Sin transacciones entre particiones:** las operaciones que afecten a varios tenants no son atómicas.
- Consultas limitadas: no hay agregaciones ni consultas ad-hoc como en un motor relacional.
- Modelo de datos desnormalizado; hay que repetir campos para consultas por criterios distintos.
- Sin índices secundarios: cualquier criterio de búsqueda distinto de la clave exige exploración de partición.

**Mitigación**

- Mantener los datos operativos mínimos y desnormalizar lo necesario.
- Si en el futuro se requieren consultas relacionales, agregaciones o transacciones multi-entidad, reevaluar (ver tabla de alternativas).

## Alternativas consideradas

| Alternativa | Motivo de descarte (por ahora) |
|---|---|
| Azure SQL / PostgreSQL | Mayor coste y operación para datos operativos simples; se reevaluará si aparecen necesidades relacionales |
| Cosmos DB | Potencia superior, pero sobredimensionado y con modelo de coste distinto al objetivo inicial |
| Estado solo en Service Bus | **Prohibido**: Service Bus no es fuente de verdad ni permite consultar historial |
| Estado en Qdrant | **Prohibido**: Qdrant es índice vectorial, no almacén transaccional |
| Redis | Pensado para datos efímeros; no como fuente de verdad duradera |

## Nota

Es una **propuesta inicial**. El punto 9 de «Cuestiones abiertas» de `docs/ARCHITECTURE.md` mantiene abierta su revisión.
