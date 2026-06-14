# Conceptos Clave de glia

Este documento detalla los conceptos fundamentales y las definiciones técnicas de la plataforma `glia`. Si sos desarrollador o querés entender la teoría detrás del diseño del sistema, este es tu punto de partida.

---

## 💾 1. El Store Canónico (Canonical Store)

El **Store Canónico** es la única fuente de verdad (Single Source of Truth) para la memoria del proyecto.

- **Formato Físico**: Almacenado como un archivo plano en formato **JSONL** (`memory.jsonl`) bajo la carpeta `.glia/` en el repositorio. Cada línea es un objeto JSON autónomo que representa un registro o cambio de estado.
- **Semántica Append-Only**: El archivo `memory.jsonl` es de **solo-escritura-al-final** (append-only). Nunca se modifican ni se eliminan líneas existentes en el disco. Cualquier mutación (edición o eliminación) se realiza agregando una nueva línea al final del archivo.
- **Identidad del Registro (Canonical ID)**: Cada memoria tiene un identificador único global (`canonical_id`) representado por un **ULID** (Universally Unique Lexicographically Sortable Identifier). Los ULID combinan aleatoriedad con ordenamiento por tiempo, lo que facilita ordenarlos cronológicamente de forma natural.

---

## 📝 2. Registros Canónicos (Canonical Records)

Un `CanonicalRecord` representa el estado de una memoria en un punto temporal específico. Sus campos principales son:

```go
type CanonicalRecord struct {
    CanonicalID   string   `json:"canonical_id"`   // ULID de la entidad
    LineULID      string   `json:"line_ulid"`      // ULID único para esta línea física en el log
    SchemaVersion int      `json:"schema_version"` // Versión de esquema (ej. 1)
    Kind          string   `json:"kind"`           // "observation", "session_summary" o "relation"
    Revision      int      `json:"revision"`       // Contador incremental de versiones (1, 2, ...)
    Supersedes    string   `json:"supersedes"`     // ID canónico de la memoria que esta línea reemplaza
    Deleted       bool     `json:"deleted"`        // Flag de eliminación (Tombstone)
    Title         string   `json:"title"`          // Título descriptivo
    Content       string   `json:"content"`        // Contenido/Cuerpo de la memoria
    ContentFormat string   `json:"content_format"` // Formato del contenido (ej. "markdown")
    Origin        Origin   `json:"origin"`         // Metadatos de procedencia original
    CreatedAt     string   `json:"created_at"`     // Timestamp UTC ISO8601 (nanosegundos estables)
    UpdatedAt     string   `json:"updated_at"`     // Timestamp UTC ISO8601 (nanosegundos estables)
    Tags          []string `json:"tags"`           // Etiquetas asociadas
    TopicKey      string   `json:"topic_key"`      // Clave conceptual temática
}
```

### El Origen (`Origin`)
Guarda la trazabilidad de dónde provino la memoria:
- **Provider**: El nombre del adaptador de origen (ej. `engram`, `claudemem`, `openspec`).
- **ProviderID**: El identificador único que tenía el registro en la herramienta externa.
- **Author**: Quién creó la memoria (nombre de usuario, correo o host local).
- **SessionID**: Identificador de la sesión de chat/trabajo si aplica.

### Tipos de registro (`kind`)

El campo `kind` distingue el tipo semántico de un registro:

| `kind` | Descripción |
| :--- | :--- |
| `observation` | Memoria / hecho observado — el tipo principal de engram y claude-mem. |
| `session_summary` | Resumen de sesión de trabajo (ej. narrativa de claude-mem). |
| `spec_artifact` | Artefacto de diseño SDD: proposal, spec, design o tasks (fuente openspec). |

---

---

## 🗂️ 3. Fuentes vs. Proveedores (Sources vs. Providers)

glia distingue dos tipos de integraciones externas:

| Concepto | Dirección | Ejemplo | Config key |
| :--- | :--- | :--- | :--- |
| **Proveedor** (`provider`) | Bidireccional — pull Y push | engram, claude-mem | `providers:` |
| **Fuente** (`source`) | Solo lectura — únicamente pull | openspec | `sources:` |

Los **proveedores** son daemons o herramientas de memoria de IA: glia les saca registros (pull) y les devuelve el store canónico (push). El flujo de datos va en ambas direcciones.

Las **fuentes** son archivos estáticos en disco: glia los lee e ingesta sus contenidos como registros canónicos, pero nunca los modifica. Son la puerta de entrada para información que ya existe en el repositorio (artefactos SDD, notas, specs) sin necesidad de ningún daemon ni red.

### La fuente `openspec`

La fuente `openspec` lee los artefactos del flujo SDD (Spec-Driven Development) desde el directorio `openspec/` del repositorio:

```
openspec/
  changes/
    <nombre-del-cambio>/
      proposal.md    → kind: spec_artifact, type: proposal
      design.md      → kind: spec_artifact, type: design
      tasks.md       → kind: spec_artifact, type: tasks
      specs/*.md     → kind: spec_artifact, type: spec
  specs/
    <dominio>/spec.md → kind: spec_artifact, type: spec
```

Cada archivo se convierte en un registro canónico con:
- `topic_key` = `sdd/<cambio>/<artefacto>` (ej. `sdd/auth/design`) para cambios activos, o `spec/<dominio>` para specs consolidadas.
- `content_format` = `"markdown"` con el texto completo del archivo.
- `origin.provider` = `"openspec"`.

La fuente es **completamente idempotente**: re-ejecutar la ingesta sobre un árbol sin cambios no produce ninguna revisión nueva (se controla por hash del contenido).

---

## 🔒 4. Advisory Locking (Bloqueo Asesor)

Para garantizar la integridad del archivo `memory.jsonl` y evitar que múltiples herramientas CLI o daemons modifiquen el archivo simultáneamente, se utiliza un sistema de **bloqueo a nivel de sistema operativo** (usando la librería `gofrs/flock` sobre el archivo `.lock` en la carpeta del store).
- **Garantía**: Un solo proceso de escritura a la vez puede abrir el store. Si otro proceso intenta abrir el store mientras está bloqueado, recibe inmediatamente un error `ErrLocked` no bloqueante.

---

## 🔌 5. Adaptadores y el Contrato de Pureza

Los adaptadores traducen memorias entre el formato nativo de un proveedor de IA (como Engram o Claude) y el formato del Store Canónico de `glia`.

### Contrato de Métodos
La interfaz `Adapter` separa rígidamente las operaciones con efectos secundarios de las transformaciones de datos puras:

| Tipo de Método | Métodos | Propiedades |
| :--- | :--- | :--- |
| **I/O (Con Efectos)** | `ListNative`, `ReadNative`, `WriteNative`, `Health` | Interactúan con la red, CLI, o sistema de archivos del proveedor. |
| **Puros (Sin Efectos)** | `ToCanonical`, `FromCanonical` | Funciones matemáticas puras. Mapean estructuras de datos en memoria sin realizar I/O. |

### Resolución de IDs (`IDMap`)
Durante el mapeo, se necesita traducir identificadores nativos de los proveedores a los `canonical_id` (ULID) del store canónico. Los adaptadores reciben una vista de lectura inmutable (`IDMap`) provista por el Store, aislando la lógica de traducción de identidades de la persistencia directa en disco.

---

## 🔄 6. El Motor de Sincronización (Sync Engine)

El motor de sincronización (`internal/sync`) se encarga de coordinar la transferencia bidireccional de registros en dos flujos fundamentales:

```
PULL: Canónico ──(To Native)──► Proveedor Nativo
PUSH: Proveedor Nativo ──(To Canonical)──► Canónico
```

### Sincronización de un Proveedor (`SyncState`)
Para evitar transferir todo el historial de memorias en cada sincronización, el Store almacena metadatos de marcas de agua (watermarks) para cada proveedor en `index.json` (`SyncState`), tales como:
- `last_pulled_at` y `last_pushed_at`: Timestamps en formato RFC3339Nano que indican cuándo se realizó la última sincronización.
- `records_pulled` y `records_pushed`: Contadores de depuración de registros transferidos.

### Integración con Git (`--commit`)
El motor de sincronización ofrece una opción de autocommit. Al completar una sincronización exitosa de forma local, ejecuta de manera automática:
```bash
git add .glia/
git commit -m "chore: sync [timestamp]"
```
Cualquier falla en los comandos Git se trata como un warning en consola y no aborta el proceso, garantizando la resiliencia técnica de la sincronización.

---

## ⚔️ 7. Detección y Resolución de Conflictos

Dado que la sincronización es bidireccional y distribuida en múltiples proveedores, pueden surgir colisiones (ej. una misma memoria modificada en Claude y en Engram de forma paralela).

### ¿Cómo se detectan los conflictos?
Durante el proceso de reconstrucción del índice (`rebuild`), el sistema analiza todas las líneas físicas del archivo `memory.jsonl`.
Un conflicto se detecta cuando existen **dos o más líneas diferentes que declaran el mismo `canonical_id` y el mismo número de `revision`**.

### Reglas de Desempate Automático (Tiebreaker)
Para que el sistema siga operando en modo lectura de forma determinista mientras el conflicto no se resuelva manualmente, el cargador del Store selecciona provisoriamente un ganador automático siguiendo el orden de prioridades:
1. **`revision` más alta** (el cambio más avanzado en la cadena).
2. **`updated_at` más reciente** (desempate por timestamp léxico).
3. **`line_ulid` más alto** (desempate por ULID físico lexicográfico).

### Resolución Manual (`Resolve`)
El conflicto se registra en el archivo `index.json` en una lista estructurada de conflictos. La resolución del conflicto consiste en:
1. Seleccionar manualmente uno de los registros en conflicto (duplicados).
2. Escribir un nuevo registro canónico que **supera** (supersedes) a la revisión conflictiva con el payload elegido, incrementando el contador de revisión.
3. Eliminar la entrada de conflicto de `index.json` y guardar los cambios.
