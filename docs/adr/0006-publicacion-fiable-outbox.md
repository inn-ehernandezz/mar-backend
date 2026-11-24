# ADR 0006 — Publicación fiable de mensajes (outbox)

- **Estado:** Aceptada
- **Fecha:** 2026-11-24
- **Contexto de la decisión:** diseño del procesamiento asíncrono

## Contexto

Al crear un trabajo de procesamiento ocurren dos cosas que deben ser consistentes entre sí:

1. Persistir el trabajo en Azure Table Storage.
2. Publicar el mensaje en Azure Service Bus para que un worker lo procese.

Si se persiste el trabajo y falla la publicación, el trabajo queda **huérfano** (nunca se procesa). Si se publica y falla la persistencia, se publica un mensaje de un trabajo **inexistente**.

No existe transacción distribuida entre Table Storage y Service Bus.

## Decisión

Aplicar el **patrón outbox** aprovechando las claves de partición definidas en el ADR 0002:

1. En una **única operación de lote transaccional dentro de la partición** (`PartitionKey = tenant_id`) se escriben:
   - la entidad `job` con estado `QUEUED`;
   - la entidad `outbox` con el mensaje a publicar (estado «pendiente»).
2. Un **relay de publicación** lee las entradas pendientes de `outbox`, las publica en Service Bus y las marca como enviadas mediante **actualización condicional por ETag**.
3. Si la publicación falla, la entrada permanece pendiente y se reintenta.

El sistema garantiza **entrega al menos una vez**: los consumidores deben ser **idempotentes** (ver `docs/ARCHITECTURE.md` §7.3 y §11.4).

## Consecuencias

**Positivas**

- No se pierden mensajes cuando la escritura del trabajo ha tenido éxito.
- No se publican mensajes sin trabajo asociado.
- No requiere coordinador de transacciones ni infraestructura adicional.

**Negativas / costes**

- Existe una **latencia** entre la creación del trabajo y su publicación, determinada por la frecuencia del relay.
- El relay es un componente más a operar y monitorizar (profundidad de `outbox` pendiente).
- **Duplicados posibles**: se toleran por idempotencia, no por deduplicación de la cola.

**Mitigación**

- El relay se ejecuta también **bajo demanda** tras crear un trabajo, para reducir la latencia en el caso normal.
- Alerta sobre entradas de `outbox` pendientes más antiguas que un umbral.

## Alternativas consideradas

| Alternativa | Motivo de descarte |
|---|---|
| Publicar directamente tras persistir | Ventana de inconsistencia: trabajo huérfano si la publicación falla |
| Publicar antes de persistir | Se pueden publicar mensajes de trabajos inexistentes |
| Transacción distribuida (2PC) | No soportada por los servicios implicados; complejidad desproporcionada |
| Cola de Service Bus como fuente de verdad | Prohibido por diseño (ver ADR 0002) |
| Barrido periódico de trabajos huérfanos | No evita la ventana de inconsistencia; solo la repara a posteriori |
