# ADR 0005 — Estructura del monorepo

- **Estado:** Aceptada
- **Fecha:** 2026-11-24
- **Contexto de la decisión:** inicio de la implementación

## Contexto

Se propuso una estructura de monorepo con el código Go bajo `backend/`:

```text
mar/
├── backend/    # go.mod aquí
├── workers/media/
├── inference/
└── ...
```

Sin embargo, el repositorio existente ya contiene el **módulo Go en la raíz**:

- `go.mod` con `module mar-backend` en la raíz.
- `main.go` en la raíz.
- Dependencias ya resueltas en `go.sum` (Gin, `qdrant/go-client`, `azblob`).

Mover el módulo a `backend/` obligaría a renombrar el módulo y a reescribir todas las rutas de import, sin ningún beneficio funcional, además de descartar trabajo previo ya presente.

## Decisión

Mantener la **raíz del repositorio como raíz del monorepo y del módulo Go**, y añadir los componentes no-Go como hermanos:

```text
mar-backend/
├── cmd/{api,worker}/
├── internal/{platform,jobs,ingestion,classification,indexing,search,rag,inference,storage}/
├── contracts/{openapi,events}/
├── workers/media/          # worker Python
├── inference/{vllm,nim,whisper,flagembedding}/
├── deploy/{docker,compose,azure}/
├── tests/
├── scripts/
├── docs/{adr}/
├── compose.yaml
└── go.mod
```

Se conserva el nombre de módulo `mar-backend`. Se documenta aquí la desviación respecto a la propuesta original para que quede constancia explícita.

## Consecuencias

**Positivas**

- No se rompen rutas de import ni el trabajo previo.
- El monorepo contiene todo (Go, Python, contratos, despliegue) en un único lugar.
- Un solo `compose.yaml` levanta el entorno completo.

**Negativas / costes**

- Si en el futuro el repositorio se separa por servicios, habrá que extraer directorios y ajustar el módulo. Es un cambio mecánico.
- La raíz acumula archivos de configuración de varios lenguajes (`go.mod`, `compose.yaml`, `workers/media/pyproject.toml`).

**Mitigación**

- Las dependencias Python se declaran dentro de `workers/media/pyproject.toml`, no en la raíz.
- Los Dockerfiles viven en `deploy/docker/` y cada worker Python incluye el suyo.

## Alternativas consideradas

| Alternativa | Motivo de descarte |
|---|---|
| Mover Go a `backend/` | Renombrado de módulo y reescritura de imports sin beneficio funcional |
| Repositorios separados por componente | Contradice la decisión de monorepo y complica la fase inicial |
