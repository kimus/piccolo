## 1. Layout model

- [x] 1.1 Add shared workspace layout helpers for root, `picoclaw/`, `integrations/`, and `runtime/` paths.
- [x] 1.2 Add container mount source/destination helpers for PicoClaw home, Piccolo integration root (`/root/.piccolo/integrations`), Devbox, Nix, and media.
- [x] 1.3 Update tests for path resolution and container mount mapping.

## 2. Workspace generation

- [x] 2.1 Update `init` generation to write `config.json`, agents, skills, and PicoClaw-owned files under `picoclaw/`.
- [x] 2.2 Create top-level `integrations/` and `runtime/{devbox,nix,media}` directories during init.
- [x] 2.3 Update `.gitignore` generation for `picoclaw/config.json`, integration auth state, and runtime cache directories.
- [x] 2.4 Update generation tests to assert the new layout.

## 3. Command path updates

- [x] 3.1 Update doctor, setup, reload, runtime, image, chat, lifecycle, and integration commands to use layout helpers for `config.json`, agent paths, and integration-name-based state paths.
- [x] 3.2 Update config backup paths to stay next to `picoclaw/config.json`.
- [x] 3.3 Update tests that currently assume `config.json` or `agents/` live at workspace root.

## 4. Docker mount updates

- [x] 4.1 Update gateway container specs to mount `<workspace>/picoclaw` to `/root/.picoclaw`.
- [x] 4.2 Add bind mounts for `<workspace>/integrations` to `/root/.piccolo/integrations`.
- [x] 4.3 Replace Docker named volume runtime mounts with workspace-visible binds from `runtime/devbox`, `runtime/nix`, and `runtime/media`.
- [x] 4.4 Update setup container specs for integrations to use the same structured mounts.
- [x] 4.5 Update Docker spec tests for all structured mount bindings.

## 5. Migration

- [x] 5.1 Add legacy flat-layout detection.
- [x] 5.2 Add an explicit migration command for moving known PicoClaw files/directories (`config.json`, `agents/`, `.security.yml`, `logs/`, `launcher-auth.db`, etc.) into `picoclaw/`.
- [x] 5.3 Preserve existing top-level `integrations/` during migration.
- [x] 5.4 Create runtime directories during migration.
- [x] 5.5 Prevent overwrites without confirmation and write a migration summary file.
- [x] 5.6 Add migration tests for fresh, legacy, conflict, and idempotent cases.

## 6. Doctor and documentation

- [x] 6.1 Update doctor to report structured layout status and legacy migration guidance.
- [x] 6.2 Update README workspace layout documentation with the full Piccolo workspace tree, ownership model, and host-to-container mounts.
- [x] 6.3 Update AGENTS.md hard constraints and implementation notes to remove the flat-layout rule.
- [x] 6.4 Document runtime cache size/location and cleanup guidance.

## 7. Verification

- [x] 7.1 Run `go test ./...`.
- [x] 7.2 Validate `restructure-workspace-layout` OpenSpec artifacts.
- [ ] 7.3 Smoke-test a migrated `.data` workspace through `doctor`, `integration setup`, `restart`, and `chat -m` where Docker is available.
