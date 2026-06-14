# glia — Centro de Documentación

Bienvenido al centro de documentación técnica de **glia**, un broker de memoria diseñado en Go que permite a un equipo compartir memoria de proyectos a través de múltiples proveedores de memoria para Inteligencia Artificial de forma unificada, estructurada y portátil.

---

## 🎯 Propósito del Proyecto

El objetivo principal de `glia` es resolver la fragmentación del almacenamiento de memoria de contexto de los diferentes proveedores de IA (como Engram, Claude-Mem, etc.). En lugar de almacenar la memoria en formatos propietarios o bases de datos centralizadas y acopladas, `glia` establece un **Store Canónico** neutral en formato **JSONL** que vive directamente en el repositorio Git del proyecto.

Esto asegura que la memoria de un proyecto:
1. **Sea portátil** e independiente de cualquier herramienta de IA concreta.
2. **Sea versionable** directamente mediante Git (incluyendo historial de modificaciones, autorías y ramas).
3. **Sea bidireccional**, permitiendo sincronizar cambios (pull y push) entre el repositorio de Git y los daemons locales de los proveedores.

---

## 🧭 Mapa de Documentación

Para entender a fondo el sistema o empezar a trabajar en él, explorá las siguientes secciones:

| Documento | Descripción | Temas Clave |
| :--- | :--- | :--- |
| [Conceptos Clave](concepts.md) | Definiciones técnicas y fundamentales del dominio de glia. | Store Canónico, Inmutabilidad, Revisión, Tombstones, Fuentes vs. Proveedores, Contratos de Adaptadores, Motores de Sincronización, Resolución de Conflictos. |
| [Arquitectura y Flujo de Datos](architecture.md) | Diseño del sistema, responsabilidades de paquetes y secuencias detalladas de I/O. | Bootstrapping del Store, xxhash fingerprinting, Dos Pasos de Decodificación (decodeLine), Flujos de Ingesta (Pull/Push), fuente openspec (`internal/source/openspec`). |
| [Guía Paso a Paso](step-by-step-guide.md) | Tutoriales prácticos para instalar, operar la herramienta y extenderla. | Setup inicial, comandos CLI, habilitar la fuente openspec, resolución de conflictos, navegación mediante la TUI, creación de nuevos adaptadores. |

---

## 🏗️ Arquitectura General

El diseño sigue una separación de capas estricta para evitar acoplamientos (los adaptadores dependen del store, pero el store jamás conoce la existencia de los adaptadores):

```
┌─────────────────────────────────────────────────────────┐
│                   AI Memory Providers                   │
│        (engram daemon/CLI, claude-mem worker)           │
└────────────┬──────────────────────────────▲─────────────┘
             │ (import/export)              │
┌────────────▼──────────────────────────────┴─────────────┐
│                    Adapter Layer                        │
│             (internal/adapter/engram, claudemem)        │
└────────────┬────────────────────────────────────────────┘
             │ (escribe registros canónicos)
┌────────────▼────────────────────────────────────────────┐
│                    Canonical Store                      │
│                  (internal/store)                       │
│    - memory.jsonl (Registro inmutable append-only)       │
│    - index.json   (Índice rápido con xxhash)            │
│    - schema.json  (Versión del esquema)                 │
└─────────────────────────────────────────────────────────┘
             ▲
             │ (ingesta de solo lectura)
┌────────────┴────────────────────────────────────────────┐
│               Static Sources (read-only)                │
│          (internal/source/openspec — artefactos SDD)    │
└─────────────────────────────────────────────────────────┘
```

Si querés profundizar en los detalles de cómo se procesa cada lectura o escritura, andá directamente al documento de [Arquitectura](architecture.md).
