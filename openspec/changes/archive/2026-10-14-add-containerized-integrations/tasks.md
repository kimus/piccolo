## 1. Manifest and descriptor model

- [x] Define `runtime.integrations` in `internal/manifest` with strict YAML parsing and deterministic rendering.
- [x] Define an internal integration descriptor schema for `piccolo.integration.v1`.
- [x] Add validation for descriptor fields: name, display name, setup command, TTY flag, setup ports, and command checks.
- [x] Add tests for manifest round-tripping and unknown-key rejection.

## 2. Integration resolution

- [x] Add alias resolution for `gws` and `google-workspace` to `github:kimus/piccolo-gws`.
- [x] Accept explicit integration references such as `github:owner/repo`.
- [x] Derive the descriptor command from the repository basename when no alias supplies it.
- [x] Warn before using unpinned remote refs.

## 3. Image build integration support

- [x] Extend the runtime image build plan to accept build-time integration package refs.
- [x] Install integration packages during Docker build, not post-start setup.
- [x] Verify declared integration commands during or immediately after image build.
- [x] Run `<integration-command> describe --json` inside a container from the built image and validate the descriptor.
- [x] Update `piccolo.yaml` with the new image and installed integration metadata only after successful build and descriptor validation.
- [x] Add unit tests for deterministic image tags including integration refs/descriptors.

## 4. CLI commands

- [x] Add `piccolo integration` command group.
- [x] Implement `piccolo integration add <alias-or-ref>` with confirmation, image build, descriptor discovery, and manifest update.
- [x] Implement `piccolo integration setup <name>` to find installed integration metadata and run its setup command.
- [x] Ensure add/setup output clearly separates image installation from integration configuration.

## 5. One-shot setup containers

- [x] Add Docker runner support for temporary one-shot containers with entrypoint/command override.
- [x] Mount the workspace at `/root/.picoclaw` for setup containers.
- [x] Resolve and pass `piccolo.yaml` container environment into setup containers.
- [x] Apply integration-declared setup ports without reusing normal gateway health/gateway ports.
- [x] Attach stdio and TTY when the descriptor requires interactive setup.
- [x] Remove setup containers after completion and propagate their exit code.

## 6. Doctor and documentation

- [x] Add doctor checks for installed integration metadata and configured image availability.
- [x] Document the user flow: `piccolo integration add gws`, `piccolo integration setup gws`, `piccolo restart`.
- [x] Document the integration author contract, including `describe --json` and setup command behavior.
- [x] Document that provider-specific `config.json` changes are integration-owned.

## 7. Verification

- [x] Add daemon-free unit tests for resolver, descriptor validation, manifest persistence, and CLI planning.
- [x] Add Docker-runner tests with fakes for image build and one-shot setup command execution.
- [x] Verify `go test ./...` passes.
