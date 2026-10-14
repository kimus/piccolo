## ADDED Requirements

### Requirement: Inventory locally built runtime images

Piccolo SHALL list the runtime images it built locally, with size and age, and SHALL mark which of them are the image in use, the recorded rollback target, and referenced by an existing container.

#### Scenario: Inventory shows what is costing disk

- **WHEN** a user asks to list runtime images
- **THEN** Piccolo reports each locally built image with its size and age
- **AND** identifies the image the workspace currently runs, the rollback target, and any image a container still references

#### Scenario: Images Piccolo did not build are left out

- **WHEN** the inventory is printed
- **THEN** images Piccolo did not build are not offered for reclamation

### Requirement: Reclaim unreferenced runtime images on request

Piccolo SHALL provide an explicit command that removes only runtime images it built that are provably unreferenced, reporting what it removed and the space reclaimed.

#### Scenario: Prune protects the image in use

- **WHEN** a user runs the prune command
- **THEN** the image referenced by `container.image` is kept

#### Scenario: Prune protects the rollback target

- **WHEN** a workspace has a recorded previous image
- **THEN** prune keeps that image, so rollback remains possible

#### Scenario: Prune protects anything a container references

- **WHEN** a stopped container still references a runtime image
- **THEN** prune keeps that image

#### Scenario: Prune needs confirmation

- **WHEN** prune is run without its confirmation flag
- **THEN** Piccolo lists the images it would remove and the bytes it would reclaim, and removes nothing

#### Scenario: Base images are never removed

- **WHEN** prune runs
- **THEN** the upstream PicoClaw images it built on are left in place

### Requirement: Unreferenced runtime images are reported, not deleted

Piccolo SHALL warn when unreferenced runtime images exceed a meaningful size and SHALL name the reclamation command, without removing anything on the user's behalf.

#### Scenario: Accumulated images trigger a warning

- **WHEN** a check runs and unreferenced runtime images exceed the threshold
- **THEN** Piccolo reports the count, the total size, and the prune command
- **AND** it removes nothing

#### Scenario: Warning never fails a check

- **WHEN** unreferenced images are reported
- **THEN** the overall result still counts as a warning, not a failure

### Requirement: Rollback survives reclamation

Image reclamation SHALL NOT remove the image needed to roll back, and SHALL state what it kept when it reports what it removed.

#### Scenario: Rollback target is protected

- **WHEN** reclamation runs on a workspace with a recorded previous image
- **THEN** that image is kept
- **AND** the report names it as the reason it was kept

#### Scenario: Rollback after reclamation still works

- **WHEN** a user rolls back following a reclamation
- **THEN** the rollback finds its target present and promotes it through the usual gate
