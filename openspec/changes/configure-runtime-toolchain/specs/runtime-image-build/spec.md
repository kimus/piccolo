## ADDED Requirements

### Requirement: Runtime image remains a generic Devbox-capable base
Piccolo's runtime image build SHALL produce a Devbox-capable PicoClaw image that can apply a workspace-configured toolchain at container runtime.

#### Scenario: Image does not require rebuilding for package list edits
- **WHEN** the user edits `runtime.toolchain.packages` or `runtime.toolchain.commands` in `piccolo.yaml`
- **THEN** Piccolo can apply the new toolchain with runtime apply or restart
- **AND** the user is not required to rebuild the image solely because the package/command list changed

#### Scenario: Image contains setup machinery
- **WHEN** Piccolo builds a runtime image
- **THEN** the image contains Devbox, required Nix build users/groups, and the Piccolo setup script
- **AND** the actual desired toolchain is read from workspace configuration at apply time

#### Scenario: Existing local runtime images remain usable
- **WHEN** an existing workspace already points at a Piccolo runtime image
- **THEN** Piccolo continues to run it when compatible
- **AND** doctor warns if the image lacks required setup capabilities for configurable runtime toolchains

### Requirement: Serve applies runtime toolchain after health
Piccolo serve/restart SHALL health-gate the gateway and then apply the runtime toolchain with clear progress output.

#### Scenario: Toolchain setup succeeds
- **WHEN** the gateway reaches `/health`
- **AND** runtime toolchain setup succeeds
- **THEN** serve reports that the gateway is running
- **AND** reports that runtime tools are ready

#### Scenario: Toolchain setup fails
- **WHEN** the gateway reaches `/health`
- **AND** runtime toolchain setup fails
- **THEN** serve reports the setup failure clearly
- **AND** includes enough recent setup output to diagnose missing packages or commands

#### Scenario: Public runtime image remains possible
- **WHEN** a future Piccolo-maintained public runtime image is used
- **THEN** Piccolo still applies the workspace `runtime.toolchain` without depending on a locally built package-specific image
