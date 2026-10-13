## Context

Piccolo currently manages a single PicoClaw gateway container and can build a local runtime image for file-handling tooling. That runtime tooling is applied after the gateway becomes healthy, which is acceptable for ordinary `exec` tools but not for integrations that PicoClaw may launch at gateway startup, such as stdio MCP servers.

The desired Google Workspace path introduces a separate project (`piccolo-gws`) that owns Google-specific setup, auth, `config.json` patching, and MCP behavior. Piccolo should therefore become a generic integration host: it installs integration packages into the gateway image, asks them to describe themselves, and runs their setup command in a container. Piccolo must not learn Google OAuth details or patch Google-specific PicoClaw config itself.

## Goals / Non-Goals

**Goals:**

- Provide a generic `piccolo integration add <alias-or-ref>` command that builds a gateway image containing a self-describing integration package.
- Provide `piccolo integration setup <name>` that executes an integration-owned setup command in a one-shot container based on the configured image.
- Keep provider-specific setup and `config.json` mutation inside the integration binary.
- Ensure integration commands exist before `picoclaw gateway` starts.
- Preserve the single gateway container runtime model for the actual assistant process.
- Record only Piccolo-owned integration build metadata in `piccolo.yaml`.

**Non-Goals:**

- Implement Google Workspace behavior in Piccolo.
- Implement a custom MCP runtime or sidecar manager.
- Patch integration-specific `config.json` entries from Piccolo core.
- Support arbitrary post-start installation for integrations required during gateway startup.
- Support remote integration marketplaces or signature verification in the first version.

## Decisions

### Integration references are installed at image build time

`piccolo integration add gws` resolves `gws` to an integration package reference such as `github:kimus/piccolo-gws`, asks for confirmation, and builds a new gateway image that installs the package during Docker build. The image is selected in `piccolo.yaml` when the build succeeds.

This avoids the race where PicoClaw starts before a stdio MCP server command exists.

### Integrations are self-describing binaries

After building the image, Piccolo runs the integration's descriptor command inside a temporary container, for example:

```sh
piccolo-gws describe --json
```

The descriptor is versioned (`piccolo.integration.v1`) and includes at minimum the integration name, display name, setup command, whether setup requires a TTY, optional setup port bindings, and command checks.

Piccolo does not fetch a separate manifest file from the repo in the initial design. The installed binary is the source of truth for behavior.

### Alias resolution is a Piccolo convenience layer

Piccolo ships a small alias map for first-party or recommended integrations:

```text
gws              -> github:kimus/piccolo-gws
google-workspace -> github:kimus/piccolo-gws
```

Full refs remain accepted. Piccolo shows the resolved source before building so users understand what third-party code is being baked into the runtime image.

### Setup runs in a one-shot container from the configured image

`piccolo integration setup gws` does not require the gateway to be running. It starts a temporary container from the currently configured image, mounts the workspace at `/root/.picoclaw`, resolves and passes the manifest environment, applies integration-declared setup port bindings, overrides the entrypoint/command, and attaches stdio for interactive setup.

The integration setup command owns:

- provider-specific prompts,
- auth and token storage,
- integration state files,
- `config.json` patching,
- any validation specific to the provider.

After setup exits successfully, Piccolo tells the user to run `piccolo restart` so the gateway starts with the updated config and image.

### `piccolo.yaml` records installed integration metadata only

`runtime.integrations` stores Piccolo-owned metadata such as integration name, source ref, resolved descriptor summary, built image association, and installed command names. It does not store provider tokens or duplicate PicoClaw runtime configuration.

### Integration packages own dependency closure

Piccolo treats an integration ref as an installable Nix flake/package. The integration package owns its dependencies. For example, `github:kimus/piccolo-gws` may package or wrap `gws` internally. Piccolo verifies declared commands, but does not maintain per-integration dependency lists.

## Risks / Trade-offs

- **Arbitrary code execution from integration refs** → Piccolo must clearly warn and require confirmation before building untrusted refs into the runtime image.
- **Unpinned refs rebuild differently** → Warn on unpinned GitHub refs and record the user-provided ref; later work can add lock/pin support.
- **Descriptor command discovery ambiguity** → Start with a simple convention: repo basename is the command (`github:kimus/piccolo-gws` -> `piccolo-gws describe --json`) and allow aliases to define the command explicitly.
- **Interactive setup complexity** → Require setup descriptor fields for `requiresTty` and ports; use the existing Docker runner attach patterns to preserve terminal behavior.
- **Config ownership confusion** → Document that `integration add` changes image/manifest only, while `integration setup` delegates config changes to the integration.
