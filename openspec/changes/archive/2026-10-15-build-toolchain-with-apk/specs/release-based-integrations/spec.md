## REMOVED Requirements

### Requirement: Build integrations from source explicitly

**Reason**: the source-build path resolved a package through Devbox/Nix, which no longer exists in the runtime image. Release installs already carry the integration's own runtime closure, so source builds have no apk equivalent and are retired rather than re-implemented.

**Migration**: install the integration from a published release with `piccolo integration add <package-ref>`. A workspace whose `piccolo.yaml` still records `source: build` fails the image build with that instruction rather than a parse error.

## MODIFIED Requirements

### Requirement: Integration identity resolvable without artifact download
Piccolo SHALL compute the desired runtime image identity for release-installed integrations from recorded release metadata (package, version, artifact digest) without downloading or extracting any artifact.

#### Scenario: Drift check downloads nothing
- **WHEN** Piccolo computes the desired image tag for a workspace with installed release integrations
- **THEN** it uses only the recorded package, version, and digest metadata
- **AND** it performs no archive download or extraction

#### Scenario: Distinct releases produce distinct tags
- **WHEN** two manifests name the same integration package with different recorded versions or digests
- **THEN** their desired image tags differ

#### Scenario: Source-installed integrations are excluded
- **WHEN** a manifest records an integration installed from source
- **THEN** the image build reports that source-built integrations are no longer supported
- **AND** it names `piccolo integration add <package-ref>` as the replacement path
