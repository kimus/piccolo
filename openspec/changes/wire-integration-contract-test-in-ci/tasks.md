## 1. Make a reduced toolchain buildable

- [ ] 1.1 Add repeatable `--command` to `piccolo image build`, replacing the effective command list like `--package` replaces the package list
- [ ] 1.2 Keep commands and packages consistent: the toolchain layer verifies each command, so document that a reduced list must name a package that provides it
- [ ] 1.3 Show the reduced set in the build plan summary
- [ ] 1.4 Test: `--command` overrides the manifest and default lists; omitted flag preserves current behaviour

## 2. Contract job

- [ ] 2.1 Add a `contract` CI job with Docker available
- [ ] 2.2 Build the image with a one-package, one-command toolchain and a pinned integration release
- [ ] 2.3 Record the built tag and pass it to the test via `PICCOLO_CONTRACT_IMAGE`
- [ ] 2.4 Run `go test -tags integration` limited to the contract test
- [ ] 2.5 Assert the job's own cost: fail if the built image exceeds a stated size budget (keeps the minimal build honest)
- [ ] 2.6 Triggers: pushes touching integration/argv/runtimeimage paths, plus a scheduled run
- [ ] 2.7 Record measured build time and image size for the job once, in the change notes

## 3. Test clarity

- [ ] 3.1 Keep the skip path explicit when `PICCOLO_CONTRACT_IMAGE` is unset, so a silent skip is impossible
- [ ] 3.2 Ensure the test reports the exact argv and the binary's rejection on failure
- [ ] 3.3 Re-run the mutation check (reorder the argv derivation) and confirm CI fails, not just local runs

## 4. Docs

- [ ] 4.1 AGENTS.md: state that argv changes are validated by the contract job, and how to run it locally
- [ ] 4.2 README: mention `--command` alongside `--package` in the image build section
- [ ] 4.3 `go build ./...`, `go vet ./...`, `go test ./...`
