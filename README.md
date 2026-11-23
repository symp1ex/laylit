# Laylit

Laylit is a Windows utility that shows the active keyboard input layout through
static keyboard-backlight colors. It supports the built-in ASUS TUF Gaming F17
FX707ZC4 keyboard and EVision/Redragon-compatible USB keyboards, including
simultaneous control of multiple keyboards.

The current EVision driver supports VID `320F`, PID `5000`. It talks directly
to the vendor HID collection and does not require OpenRGB at runtime, a WinUSB
driver, Zadig, administrator privileges, cgo, or a HID DLL.
The ASUS provider uses the installed Windows ATKACPI driver directly and
applies colors without saving them to firmware.

## Requirements

- Windows 10/11 x64
- Go 1.26 for building
- An ASUS TUF Gaming F17 FX707ZC4 or an EVision-compatible `320F:5000` keyboard
  for lighting control

Keep the standard Windows HID driver installed. Do not replace it with WinUSB.

## Launch modes

Running without arguments is reserved for the future UI and currently exits
without starting the application runtime:

```powershell
.\laylit.exe
```

SCM starts the long-running runtime explicitly with:

```powershell
.\Laylit.exe -service
```

`-service` is the only way service mode is selected; there is no automatic
SCM detection or error-driven fallback. For compatibility, the existing
`--debug` argument without a positional command still runs the automatic mode
directly and mirrors diagnostics to a console when one is available.

## Automatic runtime

At startup the application:

1. reads the installed Windows input layouts and the foreground window's
   active layout;
2. creates or reconciles `config.json`;
3. subscribes to Windows layout and device notifications;
4. discovers supported keyboards and applies the active layout's color to
   each enabled device.

The layout listener reads and deduplicates the foreground thread's HKL.
The Alt+Shift hook requests the same read when Windows does not send a Shell
notification and leaves input delivery intact. Each enabled keyboard writes
in sequence and keeps only the latest pending color. A failed connection is
retried independently. The listener can run with zero keyboards and applies
the current layout's color as supported keyboards connect.

The production `Laylit.exe` is linked with the Windows GUI subsystem. There is
currently no tray icon or other UI.

## Configuration

The file is stored at:

```text
%AppData%\laylit\config.json
```

Example:

```json
{
  "version": 2,
  "layouts": {
    "HKL-04090409": {
      "name": "English (United States)",
      "color": "#FFFFFF"
    },
    "HKL-04190419": {
      "name": "Russian (Russia)",
      "color": "#FF0000"
    }
  },
  "devices": {
    "asus-tuf:fx707zc4:internal": {
      "enabled": true,
      "colors": {
        "HKL-04190419": "#33AAFF"
      }
    }
  }
}
```

`HKL-XXXXXXXX` is the eight-hex-digit Windows input-locale identifier, not a
localized display name. The name is metadata for people and may be refreshed;
the identifier is the mapping key. Newly discovered layouts receive
`#FFFFFF`. Existing colors are preserved, and entries for temporarily absent
layouts are never deleted automatically.

Laylit migrates version 1 on automatic startup and preserves the shared
`layouts` palette. Use the device IDs printed by `info` as keys in `devices`.
The color for an enabled keyboard comes from its `colors` override, the shared
palette, or white, in that order. New keyboards are enabled by default.
Set `enabled` to `false` to leave that keyboard's lighting as it is in
automatic mode. Settings are kept while a keyboard is disconnected.

Colors are case-insensitive and must be exactly `RRGGBB` or `#RRGGBB`. An
invalid JSON document or invalid configured color stops startup with a clear
error; it is never silently rewritten. Saves use an indented JSON document and
an atomic same-directory temporary-file replacement. An unchanged config is
not rewritten.

Machine-wide logging settings are stored next to `Laylit.exe` in
`settings.json`. The service creates the file with `INFO` and 14-day retention
when it is absent.

Service and session-helper logs are stored at:

```text
<Laylit.exe directory>\logs\laylit.log
```

## One-shot CLI

The original commands remain available:

```powershell
.\laylit.exe info
.\laylit.exe set "#FF0000"
.\laylit.exe set "00FF7F"
.\laylit.exe off
.\laylit.exe set --device asus-tuf:fx707zc4:internal "#FF0000"
.\laylit.exe off --device asus-tuf:fx707zc4:internal

.\laylit.exe --debug info
.\laylit.exe --debug set "#FF0000"
.\laylit.exe --debug off
```

When more than one supported keyboard is present, `set` and `off` require
`--device <id>`. Run `info` to list the available IDs.
USB IDs include VID/PID and a unique USB serial when available. The Windows
ContainerId distinguishes keyboards with no unique serial. If Windows exposes
colliding identities and cannot distinguish the keyboards, Laylit skips them.

A GUI-subsystem build attaches to its parent console for explicit commands.
For the most predictable development/debug terminal workflow, build and use
the console entry point described below. `--debug` lists discovered HID
collections, selection details, the exact 64-byte report, and write/read
results. `--debug` automatic mode also mirrors diagnostics to the console when
one is available.

## Build

Production background binary:

```powershell
go build -ldflags="-s -w -H=windowsgui" -trimpath -o Laylit.exe ./cmd/laylit
```

Console/diagnostic binary, using the same application and CLI implementation:

```powershell
go build -o laylit-console.exe ./cmd/laylit-console
```

The split is an entry-point/linker concern only. Console visibility and
diagnostic streams are not part of the application orchestration.

## Windows service installation

Build `Laylit.exe`, keep it next to `service-cfg.bat`, and run an elevated
Command Prompt or PowerShell in that directory. The editable service settings
(`SERVICE_NAME`, display name, description, executable path, and start type)
are grouped at the top of the script. `SERVICE_NAME` must remain equal to the
Go runtime service name, `Laylit`.

```powershell
.\service-cfg.bat install
.\service-cfg.bat start
.\service-cfg.bat stop
.\service-cfg.bat uninstall
```

The registered `binPath` is equivalent to `"C:\path\to\Laylit.exe" -service`,
including correct quoting when the path contains spaces.

## Architecture

```text
cmd/laylit                  production windowsgui composition root
cmd/laylit-console          console diagnostic composition root
internal/app                automatic-mode orchestration and ports
internal/color              RGB value and HEX parsing
internal/config             config model, merge, validation, atomic JSON file
internal/layouts            layout domain/source interface
internal/layouts/windows    foreground-HKL lookup and Win32 event listener
internal/devices            RGBDevice, DeviceProvider, provider registry
internal/devices/evision    EVision detector, driver, protocol, diagnostics
internal/hid                generic Windows HID enumeration/open/read/write
internal/entry              command parsing and dependency composition
internal/winconsole         GUI-subsystem console attachment adapter
```

The application layer imports interfaces and domain values, not Win32 HID,
SetupAPI, EVision bytes, or window handles.

To add a second keyboard family:

1. implement `devices.Provider` and its `devices.RGBDevice`;
2. keep its VID/PID, collection selection, report format, and acknowledgement
   rules inside that driver package;
3. register the provider in `internal/entry` next to the EVision provider.

No layout, config, or automatic-mode code needs to change. Provider ordering is
explicit in the composition root; there is no reflection or plugin framework.

## EVision protocol compatibility

For `320F:5000`, the driver selects USB interface `1` and Usage Page `FF1C`.
It accepts any Usage ID. It requires just one matching collection per physical
keyboard and checks both input and output report sizes before use.

Static color remains exactly one 64-byte HID Output Report:

- byte `0`: Report ID `04`;
- bytes `1..2`: little-endian `uint16` sum of bytes `3..63`;
- byte `3`: set-parameter command `06`;
- byte `4`: parameter length `08`;
- byte `5`: extended-mode parameter `00`;
- bytes `8..12`: static mode `06`, brightness, speed `00`, direction `00`,
  random flag `00`;
- bytes `13..15`: red, green, blue;
- remaining bytes: zero.

Normal color uses brightness `04`. `off` remains static black with the
protocol-defined brightness `00`. No begin/end initialization handshake was
added. Every write still has a two-second limit and is followed by one
undecoded 64-byte acknowledgement read with a one-second limit; there are no
automatic retries.

## Tests

```powershell
gofmt -w cmd internal
go vet ./...
go test ./...
go test -race ./...
go build ./cmd/laylit
go build ./cmd/laylit-console
```

Tests cover HEX parsing, config creation/merge/validation/idempotence, fake-only
application orchestration and cleanup, Windows HKL normalization, VID/PID and
collection selection, report-size guards, exact static/off reports, checksum
overflow, RGB order, write/ack semantics, I/O deadlines, and Windows service
Stop/Shutdown cancellation and wait behavior.

## Limitations

- `320F:5000` is reused by several OEM keyboards. Strict collection and report
  checks reduce risk, but each firmware revision still needs a physical test.
- `RegisterShellHookWindow` is a desktop Shell API. The listener must run in an
  interactive Windows desktop session. A Windows service runs in Session 0, so
  the current layout source cannot observe the signed-in user's foreground HKL
  there and may fail during startup. The SCM lifecycle and graceful shutdown
  wiring are complete, but useful service-mode layout tracking requires a
  future per-user helper plus IPC (or another user-session-aware source).
  Windows does not document a layout-specific registered Shell notification,
  so the interactive listener resynchronizes on every delivered Shell event
  and never interprets its `wParam` or `lParam` as an HKL. Alt+Shift is covered
  by a global `WH_KEYBOARD_LL` modifier hook on that desktop because it can
  change the foreground HKL without delivering a Shell event. Cross-version
  Windows 10/11 delivery still requires integration testing.
- Windows documents that some modern IME/TSF profiles can use transient input
  locale identifiers and may not emit classic `WM_INPUTLANGCHANGE`. Classic
  keyboard layouts such as EN/RU use stable HKL identifiers; unusual IME/TSF
  profiles require integration testing.
- Only static hardware color and protocol-defined off are implemented.

## Licensing

Laylit is licensed under the [MIT License](LICENSE). Include both `LICENSE`
and [`THIRD_PARTY_NOTICES.txt`](THIRD_PARTY_NOTICES.txt) with every package that
distributes a Laylit executable, including the console build.
