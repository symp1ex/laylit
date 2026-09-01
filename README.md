# evision-rgb

Small Windows CLI for setting one static color on EVision/Redragon-compatible
USB HID keyboards with VID `320F` and PID `5000`. It talks directly to the
vendor HID collection and does not require OpenRGB at runtime, a WinUSB driver,
Zadig, or administrator privileges.

## Requirements

- Windows 10/11 x64
- Go 1.26 for building
- EVision-compatible keyboard `320F:5000`

Keep the standard Windows HID driver installed. Do not replace it with WinUSB.

## Build

```powershell
go build -o evision-rgb.exe ./cmd/evision-rgb
```

The implementation is cgo-free. It uses the Windows HID and SetupAPI APIs via
`golang.org/x/sys/windows`, so no C compiler or hidapi DLL is needed.

## Usage

```powershell
.\evision-rgb.exe info
.\evision-rgb.exe set "#FF0000"
.\evision-rgb.exe set "00FF7F"
.\evision-rgb.exe off
```

Colors are case-insensitive and must be exactly `RRGGBB` or `#RRGGBB`.

## Debug

```powershell
.\evision-rgb.exe --debug info
.\evision-rgb.exe --debug set "#FF0000"
```

Debug output lists discovered collections, the selected collection, the exact
64-byte output report, and write/read results. Normal mode does not print HID
traffic.

## Protocol reference

The protocol was taken from these OpenRGB source files:

- [`EVisionKeyboardControllerDetect.cpp`](https://github.com/CalcProgrammer1/OpenRGB/blob/master/Controllers/EVisionKeyboardController/EVisionKeyboardController/EVisionKeyboardControllerDetect.cpp)
- [`EVisionKeyboardController.cpp`](https://github.com/CalcProgrammer1/OpenRGB/blob/master/Controllers/EVisionKeyboardController/EVisionKeyboardController/EVisionKeyboardController.cpp)
- [`EVisionKeyboardController.h`](https://github.com/CalcProgrammer1/OpenRGB/blob/master/Controllers/EVisionKeyboardController/EVisionKeyboardController/EVisionKeyboardController.h)
- [`RGBController_EVisionKeyboard.cpp`](https://gitlab.com/CalcProgrammer1/OpenRGB/-/blob/d8f28b546dbdec5826774852a49a2767ffdff3cb/Controllers/EVisionKeyboardController/RGBController_EVisionKeyboard.cpp)

For `320F:5000`, OpenRGB selects USB interface `1` and Usage Page `FF1C`.
OpenRGB does not constrain the top-level Usage ID for this detector, so this
program does not guess one: `info` displays the value reported by the keyboard.
The program refuses to send if no collection, or more than one collection,
matches the confirmed interface and Usage Page. The ordinary keyboard input
collection (typically Usage Page `0001`, Usage `0006`) is never a candidate.

Static color is one 64-byte HID Output Report:

- byte `0`: Report ID `04`
- bytes `1..2`: little-endian 16-bit sum of bytes `3..63`
- byte `3`: set-parameter command `06`
- byte `4`: parameter length `08`
- byte `5`: extended-mode parameter `00`
- bytes `8..12`: static mode `06`, brightness, speed `00`, direction `00`, random flag `00`
- bytes `13..15`: red, green, blue
- remaining bytes: zero

The static-mode path in OpenRGB sends no begin/end initialization handshake.
After `hid_write`, OpenRGB performs one `hid_read`; this program mirrors that
acknowledgement read but bounds write and read operations with timeouts and
never retries automatically. `off` uses the protocol-defined brightness value
`00`, documented by OpenRGB as off, together with static black.

## Known limitations

- VID/PID `320F:5000` is reused by several EVision, Redragon, and other OEM
  keyboards. Identical IDs do not guarantee an identical firmware
  implementation. The strict interface, Usage Page, and report-size checks are
  intentional safeguards, but a physical-device test is still required for
  each keyboard revision.
- Only the OpenRGB static hardware mode and off state are implemented.
- There is no device selector when multiple physical `320F:5000` keyboards are
  connected. Sending is refused when collection selection is ambiguous.
- `info` is the first diagnostic to run for a revision that is not recognized.

## Tests

```powershell
go test ./...
```

Unit tests verify accepted/rejected HEX colors, candidate collection matching,
and every byte (including checksum and RGB order) of representative reports.
