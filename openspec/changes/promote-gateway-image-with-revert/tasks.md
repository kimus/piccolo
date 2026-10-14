## 1. Promotion sequence

- [x] 1.1 Stop-into-revert-target split from destroy: `StopKeptByName` stops without removing, then the container is renamed to the reserved name
- [x] 1.2 **Deviation from the written design, deliberate:** the plan said park the old container as `<name>.prev` and run the candidate under a temporary name, renaming the winner at the end. Implemented the equivalent with the roles swapped: the candidate takes the canonical name immediately and the old container is parked as `<name>.prev`. Reason: `Start` removes any container holding the configured name, and a stopped container keeps holding its name — so the canonical name is only safe for the thing that should be running, and crash detection then has exactly one abnormal state ("nothing on the canonical name, `.prev` exists") instead of two candidate names to reason about
- [x] 1.3 Success removes the reserved container and records the replaced image
- [x] 1.4 Stale-name removal cannot touch the reserved container (different name)
- [x] 1.5 One sequencing path: `serve`, `restart` (delegates to serve) and `integration rollback` all run it
- [x] 1.6 `restart --foreground` keeps Ctrl-C shutdown/exit-code semantics while using the same park/gate/commit-or-revert around the attached run
- [x] 1.7 **Deviation:** the candidate is NOT labelled `piccolo.role=candidate`. This Docker SDK sets labels only at create time and `ContainerUpdateOptions` has no `Label` field, so the label could never be cleared when the candidate won — a healthy long-running gateway would advertise itself as a candidate forever. Roles travel by name instead

## 2. Readiness gate

- [x] 2.1 `/health` gate kept
- [x] 2.2 Per-integration executed probe over `docker exec`, argv from the recorded status command (fallback: descriptor command, then `<binary> describe --json`; skipped when nothing is recorded)
- [x] 2.3 Requires that the binary **ran**: exec error, exit 126/127, or silence-with-failure fail the gate; a non-zero exit that produced a payload passes (unconfigured is a setup gap, not a broken image). Failure report names the binary, the probe command and its output
- [x] 2.4 Skipped entirely when the workspace has no integrations
- [x] 2.5 Test: health passes, integration cannot execute → gate fails. Verified **live** with an image whose integration binaries were removed: `exit 127 — OCI runtime exec failed: exec: "piccolo-gws": executable file not found in $PATH`

## 3. Revert path

- [x] 3.1 Gate results, probe command/output and the candidate's last 30 log lines printed before the candidate is removed
- [x] 3.2 Candidate stopped/removed, reserved container renamed back and started
- [x] 3.3 Non-zero exit with the failing stage named and the promoted image explicitly NOT adopted
- [x] 3.4 Revert failure reported separately: "promotion failed AND the previous gateway could not be restored"
- [x] 3.5 No success-shaped rollback message on a failed restore (the restore path only prints after `StartByName` succeeds)
- [x] 3.6 Tests cover each branch against the `promoter` seam, daemon-free
- [x] 3.7 Live: a failed promotion restored the previous gateway and left no `.prev` behind

## 4. Interrupted-promotion recovery

- [x] 4.1 Reserved container + nothing canonical → renamed back, started, reported as an interrupted promotion (verified live against a hand-created stray)
- [x] 4.2 Reserved container + healthy canonical → reported, service untouched, replaced by the next park
- [x] 4.3 Exactly zero or one reserved container can exist (park removes a leftover before renaming into the name)
- [x] 4.4 Surfaced in `doctor` (WARN with the repair command) and `status` (parked line)

## 5. Rollback command

- [x] 5.1 `container.previous_image` recorded on a successful promotion
- [x] 5.2 `piccolo integration rollback` verifies the target exists locally before stopping anything; `--dry-run` and `--to <image>` supported
- [x] 5.3 Rolls through the same gated promotion, so it inherits the revert path; if the gate rejects it, `piccolo.yaml` is written back to the image that survived
- [x] 5.4 Target described from image labels (base, toolchain, integration versions)
- [x] 5.5 Refuses with a clear message when there is no recorded target, when the image is gone, and when the "rollback" would land on the image already running
- [x] 5.6 Tests: target resolution (default/explicit/empty/already-running) and label description determinism; live run rolled a workspace from a good image back to the previous one and reported the recorded target
- [x] 5.7 Documented that rollback restores **service**, not configuration: the integration records still describe the newer version, so the content warning keeps reporting the difference until the choice is made durable

## 6. Constraints and verification

- [x] 6.1 Promotion never builds implicitly: a missing target stops before touching the gateway and names `piccolo image build`
- [x] 6.2 Published ports unchanged across the candidate handover (verified: rename kept bindings; the parked container was stopped, so its port was free for the candidate)
- [x] 6.3 No window with two running gateways — and no window with two writers to `/root/.picoclaw` either, since the previous gateway is stopped before the candidate starts
- [x] 6.4 Sequencing unit-tested through the runner seam, daemon-free
- [x] 6.5 `go build ./...`, `go vet ./...`, `go test ./...` green
- [x] 6.6 Live sequence check: promote healthy → promote broken → automatic revert with diagnostics → rollback to the previous image → stray-`.prev` recovery
- [x] 6.7 README documents `restart`, the gate, rollback and the interrupted-promotion states

## Notes found during implementation

- Two containers mounting the same `picoclaw/` directory collide on PicoClaw's singleton pid file: the launcher's gateway child exits with `singleton check failed: gateway is already running (PID: 17)`. That is what makes "stop before starting the candidate" mandatory rather than cosmetic — and it produced one false failure during testing when a debug container shared the mount.
- `piccolo restart` no longer stops-and-removes first: doing that discarded the only revert target a failed promotion could use.
