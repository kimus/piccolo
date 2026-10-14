## Why

Release 0.2.0 was published, and verifying it end-to-end found that the documented installer has never worked: GitHub Pages was **not enabled** on the repository (`GET /repos/.../pages` → 404, and the single Pages workflow run failed in 10 seconds because `deploy-pages` refuses when Pages is off). `https://kimus.github.io/piccolo/install.sh` returned 404 for every version of the docs.

The spec made this easy to miss. Its install scenarios use `--version 3.1.0`, a release that does not exist, so the "Install requested version" scenario could never be executed as written — a documented command nobody could run, in a capability archived as complete.

## What Changes

- Pages hosting enabled with `build_type=workflow`, and the deploy verified over HTTP instead of assumed.
- Release and install scenarios now name real tags and are exercised against the published assets.
- Added: a published binary must identify its release. The 0.1.0/0.1.1 archives were built with `-ldflags "-s -w"` only, so installing `0.1.1` and running `--version` reports `0.1.0-poc` -- a user cannot tell which binary they hold. 0.2.0 stamps `main.version`/`main.buildDate` and prints the tag and build date.
- Docs examples stop advertising a fictional version.

## Capabilities

### Modified Capabilities

- `piccolo-cli-releases`: hosting is verified, and published binaries state their own version

## Impact

- `.github/workflows/release.yml` (unchanged: it already stamps), repository Pages settings, `README.md`, `docs/index.html`
- Verified: `install.sh` and the landing page return 200; `curl | sh` installs 0.2.0 with a matching SHA-256; `--version 0.1.1` installs that pinned archive; the installed 0.2.0 binary runs `doctor` (25 `[OK]`) and `integration list` against a live workspace
- Not fixed retroactively: 0.1.0/0.1.1 assets stay unstamped, superseded by 0.2.0
