## 1. Default posture
- [x] 1.1 generate.go writes false; comment records why absence is not off
- [x] 1.2 test asserts the literal `"restrict_to_workspace": false` survives marshalling
## 2. Control and reporting
- [x] 2.1 setup/patch.go WorkspaceRestricted/SetWorkspaceRestricted; setup screen toggle
- [x] 2.2 doctor checkExecSafety warns on guard+exec and guard-only, never FAIL
- [x] 2.3 tool sync must NOT write the flag (a security setting is not a convenience write)
