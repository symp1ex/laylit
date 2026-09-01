package keyboard

import "encoding/binary"

const (
	reportSize        = 64
	reportID          = 0x04
	commandSetParam   = 0x06
	modeStatic        = 0x06
	brightnessOff     = 0x00
	brightnessHighest = 0x04
)

// buildStaticReport ports the packet layout used by OpenRGB's
// EVisionKeyboardController::SendKeyboardModeEx, SendKeyboardParameter, and
// ComputeChecksum. RGBController_EVisionKeyboard selects EVISION_KB_MODE_STATIC
// (0x06) and sends the mode color in R, G, B order.
//
// OpenRGB writes this as one 64-byte HID Output Report. Byte 0 is report ID
// 0x04; the checksum is the uint16 sum of bytes 3..63, stored little-endian in
// bytes 1..2. The static-mode path does not call the available begin/end
// helpers, so no initialization handshake is added here.
func buildStaticReport(red, green, blue, brightness byte) [reportSize]byte {
	var report [reportSize]byte
	report[0] = reportID
	report[3] = commandSetParam
	report[4] = 8 // mode parameter block length
	report[5] = 0 // extended mode parameter
	report[8] = modeStatic
	report[9] = brightness
	report[10] = 0 // speed is unused for static mode
	report[11] = 0 // direction
	report[12] = 0 // random-color flag
	report[13] = red
	report[14] = green
	report[15] = blue

	var checksum uint16
	for _, value := range report[3:] {
		checksum += uint16(value)
	}
	binary.LittleEndian.PutUint16(report[1:3], checksum)
	return report
}
