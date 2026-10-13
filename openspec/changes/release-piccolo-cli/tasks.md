## 1. Release Workflow

- [x] 1.1 Add release workflow triggered by bare version tags such as `3.1.0`
- [x] 1.2 Configure cross-compilation for darwin/linux on arm64/amd64
- [x] 1.3 Package archives with the `piccolo` binary and lightweight metadata
- [x] 1.4 Publish SHA-256 checksums with each GitHub Release
- [x] 1.5 Ensure release workflow keeps existing CI checks as prerequisites or runs equivalent tests

## 2. Static Installer and Pages

- [x] 2.1 Add a minimal static site or docs page for Piccolo installation
- [x] 2.2 Add `install.sh` that resolves latest or `--version <version>` releases
- [x] 2.3 Implement OS/architecture detection and asset selection in the installer
- [x] 2.4 Implement checksum download and verification before installing
- [x] 2.5 Implement install directory selection and PATH guidance
- [x] 2.6 Configure GitHub Pages publishing for the static site/install script

## 3. Documentation and Validation

- [x] 3.1 Update README install instructions for curl installer, pinned version install, and manual verification
- [x] 3.2 Document that the installer only installs the host `piccolo` CLI
- [x] 3.3 Add script lint/smoke checks for installer behavior where practical
- [x] 3.4 Run `go test ./...` and `openspec validate release-piccolo-cli --strict`
