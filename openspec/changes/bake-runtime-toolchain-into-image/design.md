## Context

The runtime image installs its toolchain at **serve** time into a persisted volume, and `serve` bind-mounts that volume over `/nix`:

```go
// internal/workspace/workspace.go
ContainerNix = "/nix"
func (l Layout) RuntimeBinds() []string { ... l.RuntimeNix() + ":" + ContainerNix + ":rw" ... }

// cmd/piccolo/serve.go
if runtimeimage.IsLocalRuntimeImage(m.Container.Image) {
	volumes = append(volumes, layout.RuntimeBinds()...)   // includes /nix and the Devbox profile
}
```

Anything the image baked into `/nix/store` is thereby shadowed. Measured, same image, two mount configurations:

```text
image as-is            → gws 0.22.5; piccolo-gws describe --json works
image + /nix bind      → "gws: not found"; "piccolo-gws: not found"; python3/jq MISSING
                        (1464 store entries in the image, 68 visible)
```

The binaries exist on `PATH` in the shadowed container and still cannot execute, because their ELF interpreter is one of the hidden paths — so a presence check is not a usable signal, which the companion promotion change relies on.

Integration one-shots deliberately avoid that bind:

```go
// cmd/piccolo/integration.go
func integrationSetupBinds(layout workspace.Layout) []string {
	// Do not mount the workspace runtime/nix cache over /nix ... hides the image's
	// build-time Nix store and breaks Nix wrapper shebangs ...
	return []string{layout.IntegrationBind()}
}
```

so `describe`/`state`/`call` run in an environment the gateway will never have — a pre-test of an image the gateway doesn't run, in an environment it doesn't use.

Also measured: the tag omits integration identity.

```go
tag = DefaultTag(base, dockerfile, devbox, integrationsJSON)   // bundle.Version absent (0 uses)
```

`bundle.Version` appears only as a sort tie-break, so different versions of one integration collide.

## Goals / Non-Goals

**Goals:**
- One environment definition: what a one-shot validates is what the gateway runs.
- Toolchain changes rebuild only what they must, and integration changes don't touch the toolchain layer.
- The tag answers "is this image exactly this content?"; labels answer "what is in this image?".
- Drift is visible and actionable without Piccolo ever rebuilding on its own.

**Non-Goals:**
- No automatic rebuild, and no migration or cache-cleanup code (WiP policy: warn and let the user rebuild).
- No toolchain editor command; `runtime.toolchain` stays hand-edited in `piccolo.yaml`.
- No BuildKit dependency.
- No runtime package installation inside the gateway.

## Decisions

### Toolchain layer placement

Insert `RUN devbox … install` **after** `COPY devbox.json` and **before** the integration block:

```dockerfile
COPY devbox.json            <DevboxGlobalDir>/devbox.json   # existing
RUN <toolchain install>                                      # new layer
<integration layers>                                         # unchanged
```

Docker's cache is prefix-based, so an unchanged toolchain layer is reused by every later integration change, and a package-list edit invalidates exactly the toolchain layer and everything after it. `COPY devbox.json` already precedes the insertion point, so the package list is a real cache key. Positioning the layer any later would couple toolchain reuse to integration churn.

Rejected: keeping runtime installation and mounting the cache at a different path — Nix has a single store, and a second store path means re-implementing the closure model for no gain.

### Binds and serve-time install go away

With the toolchain baked, `/nix` and the Devbox profile binds have no purpose, and deleting them is the fix for the shadowing. `piccolo-setup-tools`, `devboxInstallTimeout` and the tool-progress rendering at serve become dead code and are removed rather than left as a second path that can silently diverge.

Rejected: keeping the binds but pre-populating them. That preserves two sources of truth for one store and keeps the shadowing hazard one config edit away.

### Integration identity in the tag comes from metadata, not bytes

Add a sorted `owner/repo@version+sha256:<digest>` list to the tag inputs, taken from `Resolve` (release manifest), not from the downloaded artifact. This must hold because drift checks run on every `serve`: a check that downloaded every installed bundle (~31 MB each) just to print a warning would be worse than no check.

Ordering: entries are sorted by package, and the bundle list is already normalised that way, so add order doesn't change the tag for the same content set.

### Bundle context paths named by package

`release-integrations/bundle-<i>` becomes `release-integrations/<owner>/<repo>`, with the name sanitised the way container names already are. Positional indices shift when any earlier-sorting package is added, invalidating unrelated layers — exactly the churn this change exists to remove.

### Labels live on the image, not the container

The Dockerfile emits them at build time, so a container inherits them and they cannot drift from the image's actual content. Runtime-set labels could lie after a hand-started or renamed container, and promotion renames containers by design.

Keys: `piccolo.base`, `piccolo.toolchain`, `piccolo.integration.<sanitized package>`. Value form `<version>+<short digest>`.

Rejected: labels alone without the tag identity. Labels are queryable but not a cache key, and a container's labels can be set by whoever started it.

**Found during implementation:** the identity is carried twice — as an explicit tag input and inside the Dockerfile text through the label block. Either one alone still invalidates the cache on a version change; only removing both reintroduces the collision. That is documented at the tag computation rather than treated as a redundancy to clean up.

Note for the companion change: labels are metadata, not proof. The promotion gate must still **execute** the binaries.

### Drift is a warning at every read path

`serve`, `restart`, `status`, `doctor` compute the desired image from metadata and compare with what's in use, then print what differs and the command that fixes it. If the comparison can't be computed (offline, daemon unreachable, base image missing), say "could not verify" and continue — a warning path must never block starting the gateway.

Rejected: auto-rebuild on `restart`. It was the tempting option and is the wrong one twice over: it makes a config edit silently cost minutes of building, and it hides the fact that the toolchain list changed, which is exactly what users need to see.

## Risks / Trade-offs

- **Bigger images — measured, not estimated.** The default toolchain bake costs **5.78 GB** in one layer (total image 440 MB → ~8.3 GB); in-layer `nix-collect-garbage` recovered only ~30 MB, because libreoffice/ffmpeg/imagemagick/tesseract genuinely have that closure. The earlier estimate (the 162 MB workspace cache) was wrong precisely because that cache was never complete — which is why the gateway could not execute its own binaries. Mitigations: the layer is reused across integration-only changes, layer digests are shared across tags, and `runtime.toolchain.packages` is the user's reduction knob.
- **Toolchain changes re-download.** Accepted explicitly: no BuildKit cache mount, since `ImageBuildOptions` has no builder field and package-list changes are rare.
- **Existing workspaces stop working until rebuilt** — including their integration tools. Acceptable under the WiP no-migration policy; the drift warning is the mitigation and the reason it must be good.
- **Nothing can install into `/nix` at runtime any more.** If a future integration needs that, it's a design change, not a flag.
- **Leftover `.data/runtime/nix` becomes dead weight.** Piccolo must not delete it (user data); `doctor` may report its size.
- **Labels can be written by a buggy build.** Cross-checked by executing the binary, not by trusting the label.

## Migration Plan

Deliberately none. A workspace whose gateway lacks the toolchain is reported by the drift warning with the exact command to run; re-creating or rebuilding by hand is expected at this stage. No code reads old layouts or upgrades caches.

## Open Questions (resolved by measurement)

- ~~Should the toolchain be rooted against later GC?~~ Yes: an in-layer `nix-collect-garbage -d` runs, so unrooted binaries would be collected. Direct gcroot symlinks are used because `nix-store --add-root <path> --indirect` fails with `no operation specified` — it requires `--realise`.
- ~~Should a missing configured command warn?~~ No: it fails the build. An image must not advertise a tool it cannot run.
- Still open: should `piccolo.toolchain` resolve actual package versions at build time (costs a build-time query) rather than digesting the package list?
- Still open: is 5.78 GB acceptable as the default, or should the default package list split into a core set plus opt-in office/media tooling?
