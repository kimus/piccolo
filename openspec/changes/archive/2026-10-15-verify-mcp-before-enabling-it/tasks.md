## 1. Gate
- [x] 1.1 ApplyIntegrationPatch registers only; EnableMCPTool/MCPToolEnabled/ServerArgv split out
- [x] 1.2 dockerMCPProbe over integrationRunner; budget, half-close stdin, control-char-safe quoting
## 2. Both paths + naming
- [x] 2.1 setup Tools screen verifies before enabling; explicit default-No confirm; unverifiable ≠ pass
- [x] 2.2 probes every image that could serve the toggle; results name the image
## 3. Reporting
- [x] 3.1 doctor reachability + registration + running-image checks; stale means exists-and-not-running
- [x] 3.2 tests daemon-free via stub engine; mutation check confirms the stale-container case is caught
