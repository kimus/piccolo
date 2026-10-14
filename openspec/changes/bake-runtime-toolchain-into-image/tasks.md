## 1. Measure before changing the design

- [ ] 1.1 Record the current toolchain footprint: size of the workspace Nix cache and the number of store entries, from an existing workspace
- [ ] 1.2 Build a runtime image with the toolchain baked and record the resulting image size and first-build duration
- [ ] 1.3 Rebuild after changing only an integration and record duration, confirming the toolchain layer is reused
- [ ] 1.4 Change the toolchain package list and record the invalidation scope and duration

## 2. Integration identity in the tag

- [ ] 2.1 Add a sorted `owner/repo@version+digest` list of installed release integrations to the tag inputs
- [ ] 2.2 Source that identity from resolve-time release metadata; never download artifacts to plan
- [ ] 2.3 Stop downloading every installed bundle during a rebuild decision path
- [ ] 2.4 Remove the `--force`-style workaround for missing version churn if any exists in cache-busting
- [ ] 2.5 Test: two versions of one integration produce different tags
- [ ] 2.6 Test: install order does not change the tag for the same set
- [ ] 2.7 Test: planning computes a tag with zero artifact downloads

## 3. Toolchain baked into the image

- [ ] 3.1 Insert the toolchain install layer after the Devbox config copy and before the integration layers
- [ ] 3.2 Ensure the layer's cache key is the package list, so editing it invalidates exactly this layer and later ones
- [ ] 3.3 Confirm the built image exposes `python3`, `jq`, `gws` and `piccolo-gws` executably with no mounts at all
- [ ] 3.4 Keep the base-layer order stable so unchanged bases stay cached across rebuilds

## 4. Remove the shadowing binds and start-time install

- [ ] 4.1 Drop the workspace Nix cache bind so nothing covers the image's store
- [ ] 4.2 Drop the Devbox profile bind
- [ ] 4.3 Keep the media bind, which is unrelated to the store
- [ ] 4.4 Remove the serve-time tool installation invocation and its timeout and progress rendering
- [ ] 4.5 Delete the now-unreachable toolchain setup script from the image, or keep it only if still referenced by another command
- [ ] 4.6 Test: a container started from the built image runs the same binaries an integration one-shot runs
- [ ] 4.7 Note in docs that the old workspace cache directory becomes unused and is never deleted by Piccolo

## 5. Package-named bundle paths

- [ ] 5.1 Replace positional bundle directories with paths derived from the package reference, sanitised like container names
- [ ] 5.2 Test: adding an integration leaves existing integrations' bundle paths unchanged
- [ ] 5.3 Reject or sanitise package references that would escape the bundle root

## 6. Image content labels

- [ ] 6.1 Emit `piccolo.base` with the base image reference
- [ ] 6.2 Emit `piccolo.toolchain` with a digest of the package list
- [ ] 6.3 Emit one `piccolo.integration.<owner>/<repo>` label per installed release integration as `version+short-digest`
- [ ] 6.4 Test: labels are present after build and survive a container rename
- [ ] 6.5 Test: listing images allows selecting ones containing a given integration version

## 7. Drift reporting

- [ ] 7.1 Compute the desired image for the current workspace from metadata only
- [ ] 7.2 Compare it with the image in use and report a reason: toolchain change, integration version change, base change, or Piccolo template change
- [ ] 7.3 Warn in `serve`, `restart`, `status` and `doctor`; never rebuild implicitly
- [ ] 7.4 Name the rebuild command in the message
- [ ] 7.5 If the check cannot be computed, report "could not verify" and continue starting
- [ ] 7.6 Test: stale toolchain, stale integration, unverifiable, and up-to-date cases
- [ ] 7.7 Test: the check performs no container starts and no artifact downloads

## 8. Verification and docs

- [ ] 8.1 Update README to state the toolchain is baked in and that drift is reported, not fixed
- [ ] 8.2 Update AGENTS.md acceptance criteria for the tag-content and bind-removal rules
- [ ] 8.3 Re-check the web-console rule still holds: publishing `:18800` needs a launcher-based image, and drift reporting surfaces that case
- [ ] 8.4 Run `go build ./...`, `go vet ./...`, `go test ./...`
- [ ] 8.5 Live check on an existing workspace: rebuild by hand and confirm the gateway executes `gws` and the toolchain with no binds
