# ADR 0001 — Monolito modular con arquitectura hexagonal

- **Estado:** Aceptada
- **Fecha:** 2026-11-24
- **Contexto de la decisión:** inicio de la implementación de MAR

## Contexto

MAR debe ser un motor de conocimiento multimodal consumido por API REST. Se prevé que algunos servicios (procesamiento multimedia, inferencia) tengan perfiles de recursos muy distintos al resto, y que en el futuro existan necesidades de escalado o aislamiento que justifiquen microservicios.

Dos extremos son indeseables:

1. **Microservicios desde el primer día:** multiplica la complejidad operativa, la latencia entre servicios y los puntos de fallo, antes de conocer los límites reales.
2. **Monolito acoplado:** imposibilita la extracción posterior sin reescribir la lógica de negocio.

## Decisión

Implementar MAR como **monolito modular en Go** organizado por **contextos delimitados** (`ingestion`, `jobs`, `classification`, `indexing`, `search`, `rag`) sobre **arquitectura hexagonal (Ports & Adapters)** y **DDD pragmático**:

- Cada módulo define sus **entidades**, **casos de uso** y **puertos** (interfaces).
- Los **adaptadores** (Azure Blob, Azure Table, Service Bus, Qdrant, motores de inferencia, HTTP) implementan los puertos y se ubican en `internal/storage/`, `internal/inference/` y en la capa de exposición.
- La dependencia siempre apunta **hacia dentro**: infraestructura → casos de uso → dominio.
- Los módulos se comunican hoy por **interfaces Go en proceso**; esos mismos puertos podrán implementarse mañana con adaptadores REST, gRPC o eventos de Service Bus.
- Un módulo **no** accede a los repositorios internos de otro módulo.

## Consecuencias

**Positivas**

- La lógica de negocio no conoce SDK de Azure, Qdrant ni motores de IA: los modelos y los almacenes son sustituibles.
- La extracción a microservicio se reduce a implementar el puerto con transporte remoto.
- Testabilidad alta: los puertos permiten dobles en memoria.

**Negativas / costes**

- Más indirección que un monolito en capas tradicional.
- Riesgo de sobreingeniería si se crean puertos o interfaces sin una segunda implementación o una necesidad real.

**Mitigación**

- No se crean interfaces ni subpaquetes vacíos: aparecen cuando tienen responsabilidad real.
- Se revisa periódicamente que no haya abstracciones sin uso.

## Alternativas consideradas

| Alternativa | Motivo de descarte |
|---|---|
| Microservicios desde el inicio | Complejidad operativa prematura; límites aún no validados con carga real |
| Monolito en capas (handlers → services → DAO) | Acoplamiento a infraestructura; dificulta la extracción futura |
| Clean Architecture estricta con capas rígidas | Ceremonia excesiva para el tamaño actual del proyecto |
