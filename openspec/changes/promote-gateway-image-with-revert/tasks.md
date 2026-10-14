## 1. Promotion sequence

- [ ] 1.1 Split stop-into-revert-target from destroy: stop the gateway, then rename it to the reserved previous name
- [ ] 1.2 Start the target image under the canonical name, labelled as a candidate
- [ ] 1.3 On success: remove the reserved container, clear the candidate label, record the replaced image as the rollback target
- [ ] 1.4 Ensure the existing stale-name removal cannot delete the reserved container
- [ ] 1.5 Reuse one promote entry point for `serve`, `restart` and rollback so there is a single sequencing path
- [ ] 1.6 Confirm `restart --foreground` keeps its Ctrl-C shutdown semantics while using the same sequence

## 2. Readiness gate

- [ ] 2.1 Keep the existing `/health` gate
- [ ] 2.2 Add a per-integration executed probe over `docker exec`, deriving the binary and command from the descriptor already collected at add time
- [ ] 2.3 Require an executable result, not a PATH lookup, and report the command plus its output on failure
- [ ] 2.4 Skip the probe set entirely when the workspace has no integrations
- [ ] 2.5 Test: health passes but the integration binary cannot execute — promotion fails and reverts

## 3. Revert path

- [ ] 3.1 Capture gate results, health response and recent container logs before removing the candidate
- [ ] 3.2 Stop and remove the candidate, restore the reserved container's name, start it
- [ ] 3.3 Report non-zero with both the failure reason and the restored image
- [ ] 3.4 Handle revert failure explicitly: state that neither gateway is running and name the next command
- [ ] 3.5 Never report a successful rollback that did not restore service
- [ ] 3.6 Test each branch against the runner seam without a daemon

## 4. Interrupted-promotion recovery

- [ ] 4.1 Detect the reserved container with nothing on the canonical name, restore and start it, report the interruption
- [ ] 4.2 Detect a stray reserved container alongside a healthy gateway, report it, leave service untouched
- [ ] 4.3 Clean the reserved container after a successful promote so exactly zero or one can exist
- [ ] 4.4 Surface the same conditions in `doctor` and `status`

## 5. Rollback command

- [ ] 5.1 Record the previously running image in the manifest on successful promote
- [ ] 5.2 Add `piccolo integration rollback` resolving that image and verifying it exists before stopping anything
- [ ] 5.3 Route rollback through the same gated promotion, so it inherits revert-on-failure
- [ ] 5.4 Describe the target using image labels: base plus installed integration versions
- [ ] 5.5 Refuse with a clear message when there is no recorded target or the image is gone
- [ ] 5.6 Test: success, missing image, and no-history cases

## 6. Constraints and verification

- [ ] 6.1 Enforce that promotion never builds an image implicitly; missing target stops before touching the gateway
- [ ] 6.2 Keep published ports unchanged across the candidate rename
- [ ] 6.3 Confirm no window where two gateways run simultaneously
- [ ] 6.4 Unit-test the sequencing through the runner seam; keep it daemon-free
- [ ] 6.5 Run `go build ./...`, `go vet ./...`, `go test ./...`
- [ ] 6.6 Live sequence check: promote good → promote broken image → automatic revert with diagnostics → rollback to the previous image
- [ ] 6.7 Document `restart`, rollback and the interrupted-promotion states in README
