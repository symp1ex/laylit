# evision-rgb

Small Windows utility that shows the active keyboard input layout through a
static RGB color on an EVision/Redragon-compatible USB HID keyboard. Version 2
adds an event-driven automatic mode while preserving the original `info`,
`set`, and `off` diagnostics.

The current EVision driver supports VID `320F`, PID `5000`. It talks directly
to the vendor HID collection and does not require OpenRGB at runtime, a WinUSB
driver, Zadig, administrator privileges, cgo, or a HID DLL.

## Requirements

- Windows 10/11 x64
- Go 1.26 for building
- An EVision-compatible `320F:5000` keyboard

Keep the standard Windows HID driver installed. Do not replace it with WinUSB.

## Automatic mode

Run without a positional command:

```powershell
.\evision-rgb.exe
```

At startup the application:

1. reads the installed Windows input layouts and the foreground window's
   active layout;
2. creates or reconciles `config.json`;
3. opens the registered supported RGB device;
4. immediately applies the active layout's color;
5. treats delivered Windows Shell notifications as event-driven signals to
   resynchronize the foreground thread's actual layout;
6. uses a low-level modifier hook to request the same resynchronization after
   Alt+Shift, which does not reliably produce a Shell notification;
7. serially applies the newest notified layout color until the process exits.

There is no timer or layout polling loop. Device writes are serialized. If
several events arrive while HID I/O is in progress, the event adapter keeps the
newest pending layout. A runtime `SetColor` failure is logged and the listener
continues; startup failures are fatal. The Alt+Shift hook observes only the
Alt/Shift modifier chord, never suppresses input, and does not infer a layout
from the keys: the listener always reads and deduplicates the foreground
thread's actual HKL.

The production `evision-rgb.exe` is linked with the Windows GUI subsystem, so
the no-argument mode does not create a console window. There is no tray icon or
other UI.

## Configuration

The file is stored at:

```text
%AppData%\evision-rgb\config.json
```

Example:

```json
{
  "version": 1,
  "layouts": {
    "HKL-04090409": {
      "name": "English (United States)",
      "color": "#FFFFFF"
    },
    "HKL-04190419": {
      "name": "Russian (Russia)",
      "color": "#FF0000"
    }
  }
}
```

`HKL-XXXXXXXX` is the eight-hex-digit Windows input-locale identifier, not a
localized display name. The name is metadata for people and may be refreshed;
the identifier is the mapping key. Newly discovered layouts receive
`#FFFFFF`. Existing colors are preserved, and entries for temporarily absent
layouts are never deleted automatically.

Colors are case-insensitive and must be exactly `RRGGBB` or `#RRGGBB`. An
invalid JSON document or invalid configured color stops startup with a clear
error; it is never silently rewritten. Saves use an indented JSON document and
an atomic same-directory temporary-file replacement. An unchanged config is
not rewritten.

Automatic-mode logs are stored at:

```text
%LocalAppData%\evision-rgb\evision-rgb.log
```

## One-shot CLI

The original commands remain available:

```powershell
.\evision-rgb.exe info
.\evision-rgb.exe set "#FF0000"
.\evision-rgb.exe set "00FF7F"
.\evision-rgb.exe off

.\evision-rgb.exe --debug info
.\evision-rgb.exe --debug set "#FF0000"
.\evision-rgb.exe --debug off
```

A GUI-subsystem build attaches to its parent console for explicit commands.
For the most predictable development/debug terminal workflow, build and use
the console entry point described below. `--debug` lists discovered HID
collections, selection details, the exact 64-byte report, and write/read
results. `--debug` automatic mode also mirrors diagnostics to the console when
one is available.

## Build

Production background binary:

```powershell
go build -ldflags "-H=windowsgui" -o evision-rgb.exe ./cmd/evision-rgb
```

Console/diagnostic binary, using the same application and CLI implementation:

```powershell
go build -o evision-rgb-console.exe ./cmd/evision-rgb-console
```

The split is an entry-point/linker concern only. Console visibility and
diagnostic streams are not part of the application orchestration.

## Architecture

```text
cmd/evision-rgb             production windowsgui composition root
cmd/evision-rgb-console     console diagnostic composition root
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
It deliberately does not constrain Usage ID. It refuses to send when zero or
more than one collection matches and validates both input and output report
sizes before use.

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
go build ./cmd/evision-rgb
go build ./cmd/evision-rgb-console
```

Tests cover HEX parsing, config creation/merge/validation/idempotence, fake-only
application orchestration and cleanup, Windows HKL normalization, VID/PID and
collection selection, report-size guards, exact static/off reports, checksum
overflow, RGB order, write/ack semantics, and I/O deadlines.

## Limitations

- `320F:5000` is reused by several OEM keyboards. Strict collection and report
  checks reduce risk, but each firmware revision still needs a physical test.
- `RegisterShellHookWindow` is a desktop Shell API. The listener must run in an
  interactive Windows desktop session, not Session 0 or a service. Windows does
  not document a layout-specific registered Shell notification, so the listener
  resynchronizes on every delivered Shell event and never interprets its
  `wParam` or `lParam` as an HKL. Alt+Shift is covered by a global
  `WH_KEYBOARD_LL` modifier hook on that desktop because it can change the
  foreground HKL without delivering a Shell event. Cross-version Windows 10/11
  delivery still requires integration testing.
- Windows documents that some modern IME/TSF profiles can use transient input
  locale identifiers and may not emit classic `WM_INPUTLANGCHANGE`. Classic
  keyboard layouts such as EN/RU use stable HKL identifiers; unusual IME/TSF
  profiles require integration testing.
- There is no device selector when multiple matching EVision RGB collections
  exist; ambiguity is intentionally refused.
- Only static hardware color and protocol-defined off are implemented.
