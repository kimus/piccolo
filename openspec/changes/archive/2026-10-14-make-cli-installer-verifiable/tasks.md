## 1. Enable hosting

- [x] 1.1 Pages created with `build_type: workflow` (it had never been enabled; API returned Not Found)
- [x] 1.2 Pages workflow deployed successfully after enablement (previously its only run failed in 10s)
- [x] 1.3 `https://kimus.github.io/piccolo/` and `/install.sh` both return 200

## 2. Exercise the documented commands, not the imagined ones

- [x] 2.1 `curl -fsSL .../install.sh | sh -s -- --dir <tmp>` installs 0.2.0, checksum `OK`
- [x] 2.2 `--version 0.1.1` downloads and installs that pinned release
- [x] 2.3 The docs example used `--version 3.1.0`, a release that does not exist, so the scenario could never be run; examples now name real tags
- [x] 2.4 Unsupported-platform verified by shimming `uname`: FreeBSD/sparc64 exits 1 with "unsupported OS: freebsd", Darwin/sparc64 exits 1 with "unsupported architecture: sparc64"

## 3. Installed binary is functional

- [x] 3.1 `--version` prints `piccolo version 0.2.0 (built <date>, go1.26.5)`
- [x] 3.2 `doctor` against a live workspace: 25 `[OK]`, exit 0
- [x] 3.3 `integration list` reads the manifest and shows gws 0.3.4

## 4. Findings worth keeping

- [x] 4.1 Release 0.1.0/0.1.1 assets were built with `-ldflags "-s -w"` and no stamp, so they self-report `0.1.0-poc`; not repaired retroactively, superseded by 0.2.0
- [x] 4.2 A capability can be archived as complete while its URL is 404: the spec scenario named a fictional version, so nobody could ever run it. Scenarios must reference things that exist.

## 5. Checks

- [x] 5.1 `openspec validate --strict`
- [x] 5.2 README/docs and spec examples agree on the same version
