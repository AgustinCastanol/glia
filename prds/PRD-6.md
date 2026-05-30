
# PRD-6 - per-provider project naming

## Context

Currently, glia uses a single project name for all providers. This PRD proposes to allow each provider to have its own project name, which will be used when storing and retrieving memories from that provider.

## Goals

- Allow each provider to have its own project name
- Use the provider-specific project name when storing and retrieving memories from that provider
- If no provider-specific project name is specified, use the project name from the config.yaml

## Non-Goals

- Allow multiple project names per provider (this will be addressed in a future PRD)