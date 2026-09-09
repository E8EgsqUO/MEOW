# script/

- `build-release.sh` — cross compile release binaries into the repository root.
- `update-chinaip.sh` — regenerate `chinaip_data.go` from the APNIC delegation
  statistics. Development-time only; the table is compiled into the binary, so
  nothing needs updating at runtime.
- `set-version.sh` — set the version string in `config.go` and `README.md`.
- `log-group-by-client.sh` — group a debug log by client connection.

## meow-taskbar.exe

A Windows tray launcher that starts MEOW minimised. Copied from `goagent.exe`,
with the string table and icon replaced using Resource Hacker.

Thanks to @phuslu for the original taskbar project.

`MEOW.ico` and `MEOW.png` are its icon sources.
