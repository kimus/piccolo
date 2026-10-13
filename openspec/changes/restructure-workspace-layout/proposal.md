## Why

The current workspace root doubles as PicoClaw home, integration state, and runtime cache metadata, which makes test workspaces hard to understand and makes Docker volumes invisible to users. Piccolo should own a clearer workspace layout that separates Piccolo launch config, PicoClaw runtime data, integration state, and local runtime caches.

## What Changes

- **BREAKING**: Change the workspace layout from mounting the workspace root as `/root/.picoclaw` to a structured layout with explicit subdirectories.
- Store PicoClaw-owned runtime state under `picoclaw/`.
- Store integration-owned state under `integrations/` outside PicoClaw home, mounted into PicoClaw home for compatibility.
- Store local runtime cache/state under `runtime/` instead of relying only on opaque Docker named volumes.
- Update `init`, `doctor`, `serve`, `restart`, `reload`, `status`, `logs`, `chat`, `runtime`, `image`, `setup`, and `integration` flows to use the new paths.
- Add migration support for existing flat workspaces, including the `.data` test workspace style.

Target layout:

```text
<workspace>/
  piccolo.yaml
  .gitignore

  picoclaw/
    config.json
    agents/
    skills/
    auth.json
    workspace/
    .picoclaw.pid

  integrations/
    gws/
      auth/
      backend.json

  runtime/
    devbox/
    nix/
    media/
```

Container paths:

```text
/root/.picoclaw          # PicoClaw home (from workspace/picoclaw)
/root/.piccolo           # Piccolo-owned integration/runtime state root
/root/.piccolo/integrations/gws
```

## Capabilities

### New Capabilities
- `structured-workspace-layout`: Defines Piccolo's workspace directory layout, container mounts, migration behavior, and path resolution rules.

### Modified Capabilities

## Impact

- Workspace generation and migration become non-trivial because `config.json` and agent files move under `picoclaw/`.
- Docker launch specs change from one workspace bind mount to several explicit bind mounts.
- Runtime cache storage moves from named Docker volumes to workspace-visible directories.
- Existing workspaces require migration or compatibility handling.
- Documentation and AGENTS guidance must be updated to replace the old flat-layout rule.
