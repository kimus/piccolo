## 1. Generate an apk toolchain layer

- [x] 1.1 One `apk add --no-cache --update <packages>` layer plus a per-command verification loop (unverified command fails the build by name, exit 3)
- [x] 1.2 Removed: `nixbld` group + 32 users, `curl … devbox` installer, `COPY devbox.json`, Nix `ENV PATH`, `devbox version`, `devbox shellenv`, `nix-collect-garbage`, gc-root symlinks, and the `/usr/local/bin` tool copies (`/usr/local/bin` fell from 316 MiB of copies to 78 MiB of bundle binaries)
- [x] 1.3 Bootstrap trimmed to `bash curl ca-certificates tar git coreutils` (`shadow` created `nixbld`; `xz` unpacked Nix tarballs)
- [x] 1.4 Default packages are apk names: `tesseract-ocr` + `tesseract-ocr-data-eng`, `yq`, `libxml2-utils`; full parity otherwise (17 packages)
- [x] 1.5 `7za` dropped from default commands — verified absent from both Alpine `p7zip` and `7zip` (they provide `7z`, `7zz`, `7zr`)
- [x] 1.6 Toolchain layer still precedes integration layers (test asserts the index order)
- [x] 1.7 `DevboxJSON`/`IntegrationsJSON`/`Integrations`/`DefaultDevboxJSON`/`renderDevbox`/`renderIntegrationsDevbox`/`DevboxGlobal*`/`*GCRoot` removed; `ContextTar` ships the Dockerfile + bundles
- [x] 1.8 Bundle layer still unpacks each release archive's own `/nix/store` closure — verified `piccolo-gws 0.3.4` runs in the new image with **no Nix installed**

## 2. Retire source-build integrations

- [x] 2.1 `piccolo integration build` command and its build path removed
- [x] 2.2 `source: build` still parses; a **rebuild** refuses it with the `integration add` remedy, while **drift checks keep working** (an already-built image still runs). Corrected mid-implementation: the first cut refused in `runtimeplan.Plan`, which would have turned a warning into a broken `serve`
- [x] 2.3 The refusal excludes the package being replaced, because `piccolo integration add <pkg>` is the remedy — verified live: refusal → add → rebuild permitted
- [x] 2.4 `upgrade` no longer tells users to run the deleted `integration build`

## 3. Wording that promised the old mechanism

- [x] 3.1 `setup.go` Devbox mentions → toolchain; sync summary no longer claims Devbox config
- [x] 3.2 `manifest.Toolchain` documents apk names and version pinning
- [x] 3.3 Generated skill: no `7za`, no `piccolo-setup-tools`, missing tool → report and rebuild host-side. Test name `TestSync…AddsDevboxJSON` renamed to what it actually asserts
- [x] 3.4 AGENTS.md + README rewritten with the measured numbers (README claimed 8.3 GB / 5.78 GB toolchain / ~40s upgrade rebuild)

## 4. Tests and live verification

- [x] 4.1 Unit tests rewritten: apk install, verification loop, banned-token list (devbox/nixbld/shellenv/gcroots), layer order, pin passthrough, determinism
- [x] 4.2 Context tar asserted to contain no devbox config files
- [x] 4.3 Source-build refusal asserted at unit level and verified at CLI level
- [x] 4.4 `go build`, `go vet`, `go test ./...` green
- [x] 4.5 **Dockerfile validity test added**: `RUN set -eu\n && …` (missing continuation backslash) passed every unit test and failed only in the daemon. New test asserts any line continuing a command follows a backslash; mutation-checked (removing the backslash makes it fail)
- [x] 4.6 Real builds: workspace image `5f6018fa00b8` in **58s**, **2.08 GB** (was 8.26 GB, ~6m50s); second workspace `7aa88f9354dc` in **60s**, **2.20 GB** — identical toolchain layer, deterministic across workspaces
- [x] 4.7 All 23 advertised commands resolve in both images; `nix` and `devbox` absent
- [x] 4.8 Functional chain in the **running** gateway: pandoc → docx → soffice → pdf → pdftotext extracted the marker value; OCR `4217` read back from a rendered page; LibreOffice 7.6.7.2, pdftotext 24.02.0, tesseract 5.5.0
- [x] 4.9 Contract test passes against the apk image; `doctor` clean; `restart` promoted with rollback recorded
