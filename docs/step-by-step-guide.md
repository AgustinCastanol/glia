# Guía Paso a Paso: Operación y Extensibilidad

Esta guía describe de manera práctica cómo preparar, operar y extender `glia`. Está orientada tanto a usuarios de la herramienta como a desarrolladores que deseen agregar nuevos proveedores de memoria.

---

## 📋 1. Requisitos Previos

Antes de comenzar, asegurate de tener instalado en tu sistema:
- **Go**: Versión 1.26 o superior.
- **Git**: Configurado en la terminal de tu sistema.
- **Engram CLI / Daemon** (Opcional): Si vas a utilizar el proveedor de Engram. Debe estar disponible en tu `PATH` y el daemon ejecutándose localmente (puerto `7437`).
- **Claude-Mem Daemon** (Opcional): Si vas a utilizar el proveedor de Claude. Debe estar ejecutándose localmente (puerto `37701`).

---

## 🛠️ 2. Compilación e Instalación

Para compilar la CLI e instalarla en tu entorno local:

1. Cloná o posicionate en la raíz del repositorio.
2. Compilá el binario usando Go:
   ```bash
   go build -o glia ./cmd/glia
   ```
3. Mové el binario compilado a una carpeta dentro de tu variable de entorno `PATH` (ej. `/usr/local/bin` o `$HOME/go/bin`) o ejecutalo localmente desde la raíz:
   ```bash
   ./glia --help
   ```

---

## 🚀 3. Guía de Uso del Usuario

### Paso 1: Inicializar un Repositorio (`init`)
Para comenzar a trackear memorias en tu proyecto de Git, tenés que inicializar la estructura ejecutando:
```bash
./glia init
```
**¿Qué sucede por detrás?**
1. Crea la carpeta oculta `.glia/` en la raíz del repositorio Git actual.
2. Bootstrapea el archivo `schema.json` indicando la versión de esquema del motor.
3. Genera el archivo de configuración por defecto `.glia/config.yaml` para habilitar y configurar los proveedores.

### Paso 2: Verificar el Estado de Sincronización (`status`)
Para saber si tus proveedores están activos y si existen colisiones pendientes de resolución:
```bash
./glia status
```
Este comando te mostrará:
- El estado de salud (Health) de cada adaptador (ej. `engram: OK`, `claudemem: OK`).
- El listado de conflictos detectados indicando el `canonical_id`, la versión de revisión y la cantidad de duplicados encontrados en el log.

### Paso 3: Sincronizar Memorias (`sync`)
Para sincronizar las memorias locales del store con tus herramientas externas de IA:
```bash
./glia sync --project "mi-proyecto" --commit
```
- El parámetro `--project` filtra las memorias específicas de ese proyecto (ignorando las de carácter personal).
- El flag `--commit` agregará automáticamente los cambios de la carpeta `.glia/` al área de staging de Git y creará un commit descriptivo de la sincronización.

### Paso 4: Navegar por la Interfaz de Terminal (`tui`)
Para buscar, leer y explorar las memorias indexadas de forma interactiva:
```bash
./glia tui
```
- Usá las **flechas del teclado (arriba/abajo)** para moverte por la lista de memorias.
- Presioná **Enter** para abrir el visor de detalles a pantalla completa de la memoria seleccionada.
- Usá la tecla **Esc** o **q** para salir o regresar al menú anterior.

---

## ⚔️ 4. Guía Paso a Paso para Resolver Conflictos

Si dos adaptadores modifican la misma memoria de forma paralela en una revisión (ej. la revisión 3), el store entrará en estado de conflicto. Seguí estos pasos para solucionarlo:

### Paso A: Identificar el Conflicto
Ejecutá `status --conflicts` para ver los detalles del conflicto:
```bash
./glia status --conflicts
```
Verás una salida similar a:
```
CONFLICTOS DETECTADOS:
- Canonical ID: 01HZZZZZZZZZZZZZZZZZZZAAAA
  Revisión: 3
  Duplicados: 2
  Detectado en: 2026-05-22T23:00:00Z
```

### Paso B: Consultar los Duplicados en Conflicto
La salida de `status --conflicts` ya lista cada duplicado con su índice (1-based), su `revision` y su proveedor de origen. Para inspeccionar el contenido completo de los registros del store y contrastar los payloads, usá `show` con salida JSON:
```bash
./glia show --json
```
Esto te permitirá contrastar los contenidos de cada duplicado (por ejemplo, el duplicado 1 provisto por `engram` vs el duplicado 2 provisto por `claudemem`) antes de decidir cuál conservar.

### Paso C: Resolver el Conflicto (`sync resolve`)
Una vez decidido qué contenido es el correcto, ejecutá `sync resolve` con el ID canónico de la memoria y el índice del duplicado ganador vía `--keep` (1-based, obligatorio):
```bash
./glia sync resolve 01HZZZZZZZZZZZZZZZZZZZAAAA --keep 2
```
**¿Qué sucede al ejecutar resolve?**
1. El motor recupera el payload del duplicado número 2 desde el archivo JSONL.
2. Escribe una nueva línea al final del archivo `memory.jsonl` con una nueva revisión (ej. revisión 4) que **supera** (supersedes) a la revisión 3.
3. Elimina la alerta de conflicto de `index.json` de forma definitiva.

---

## 🔌 5. Guía del Desarrollador: Crear un Nuevo Adaptador

Si querés agregar soporte para un nuevo proveedor de memoria (ej. `my-ai-memory`), tenés que seguir este paso a paso técnico.

### Paso A: Definir la Estructura en un Nuevo Paquete
Crea la carpeta `internal/adapter/mymemory/` y define tu adaptador struct:

```go
package mymemory

import (
    "context"
    "time"
    "github.com/agustincastanol/glia/internal/adapter"
    "github.com/agustincastanol/glia/internal/store"
)

type MyMemoryAdapter struct {
    // Agregá aquí tus clientes HTTP, clientes de base de datos o interfaces mockeables.
}

func New() *MyMemoryAdapter {
    return &MyMemoryAdapter{}
}
```

### Paso B: Implementar la Interfaz `adapter.Adapter`
Debés implementar de forma estricta los 8 métodos del contrato:

```go
// 1. Name: identificador único del proveedor.
func (a *MyMemoryAdapter) Name() string {
    return "mymemory"
}

// 2. Health: prueba si el proveedor está activo y en línea.
func (a *MyMemoryAdapter) Health(ctx context.Context) error {
    // Realizá un ping HTTP o comando rápido de verificación.
    return nil
}

// 3. ListNative: retorna los IDs de las memorias actualizadas en el proveedor.
func (a *MyMemoryAdapter) ListNative(ctx context.Context, project string, since time.Time) ([]adapter.NativeID, error) {
    var ids []adapter.NativeID
    // Consume la API del proveedor, filtrando por proyecto y marcas de tiempo.
    return ids, nil
}

// 4. ReadNative: lee el registro nativo detallado por su ID.
func (a *MyMemoryAdapter) ReadNative(ctx context.Context, id adapter.NativeID) (adapter.NativeRecord, error) {
    // Recupera la información nativa del proveedor.
    return nil, nil
}

// 5. ToCanonical: función pura que transforma el registro nativo a CanonicalRecord.
func (a *MyMemoryAdapter) ToCanonical(native adapter.NativeRecord, idmap adapter.IDMap) (store.CanonicalRecord, error) {
    // NOTA: No realices I/O en este método. Traducí los campos de forma pura.
    // Usa idmap para traducir IDs nativos a IDs canónicos si ya existen.
    return store.CanonicalRecord{
        Kind:          "observation",
        ContentFormat: "markdown",
        // Completar campos...
    }, nil
}

// 6. FromCanonical: función pura que transforma un CanonicalRecord al formato nativo.
func (a *MyMemoryAdapter) FromCanonical(canonical store.CanonicalRecord) (adapter.NativeRecord, error) {
    // NOTA: No realices I/O en este método.
    return nil, nil
}

// 7. WriteNative: escribe la memoria nativa en el proveedor.
func (a *MyMemoryAdapter) WriteNative(ctx context.Context, record adapter.NativeRecord) (adapter.NativeID, error) {
    // Realiza la escritura en el backend del proveedor.
    // IMPORTANTE: Asegurá la idempotencia (update-in-place si ya existe por origin).
    return "new-native-id", nil
}

// 8. SupportedKinds: indica qué tipos de memorias puede procesar.
func (a *MyMemoryAdapter) SupportedKinds() []string {
    // Retorna slice vacío si soporta todos ("observation", "session_summary", etc.)
    return []string{"observation"}
}
```

### Paso C: Registrar el Adaptador en el Motor
Modificá el punto de entrada de la CLI (ej. `cmd/glia/cmd/root.go` o donde se configuren las dependencias del motor) para registrar tu adaptador en el mapa del motor de sincronización:

```go
// Ejemplo de registro en la inicialización:
adapters := map[string]adapter.Adapter{
    "engram":    engramAdapter,
    "claudemem": claudeAdapter,
    "mymemory":  mymemory.New(), // Tu nuevo adaptador
}
```
y actualizá el archivo de configuración `config.yaml` de tus repositorios para incluir `mymemory` en la lista de proveedores activos.
