## 1. Detection

- [x] 1.1 `isBundleJunk` skips `._*`, `.DS_Store`, `Thumbs.db` when deriving commands from `bin/`
- [x] 1.2 Test: an archive with `bin/piccolo-math` + `bin/._piccolo-math` + `.DS_Store` yields exactly one command

## 2. Install

- [x] 2.1 Generated bundle install loop skips the same names before `install -m 0755`
- [x] 2.2 Test: the rendered Dockerfile carries the residue guard
- [x] 2.3 Live: installing the second integration (a macOS-packaged bundle that really contains a sidecar) failed before the fix with `command -v '._piccolo-math'` and now builds clean

## 3. Regression

- [x] 3.1 `go build ./...`, `go vet ./...`, `go test ./...`
- [x] 3.2 Multi-integration install, promote, status, and removal all exercised live with two release packages installed at once
