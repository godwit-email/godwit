# Native plugin P0 experiments (throwaway)

For `docs/superpowers/specs/2026-10-14-native-plugins-design.md` §6 P0.
Not part of the Godwit module; delete the branch once the results are in the design.

- `plugin/`: DLX (OwO-Network/DLX at `0f2b50d`, cloned to `dlx/`) behind ABI v1 (`abi.h`), plus `probe.sleep` and `probe.crash`.
- `host/`: one `CGO_ENABLED=0` binary. `plugin-host` checks the sha256, loads the library with purego (`syscall.LoadLibrary` on Windows) and serves JSON lines on stdio; with no subcommand it is the driver that core would be.
- `.github/workflows/native-p0-e3.yml` runs it on Linux and Windows (amd64 and arm64).

macOS results (E1, E2, E4) are in the design document.
