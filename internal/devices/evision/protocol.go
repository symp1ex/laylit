package evision

import "encoding/binary"

const (
	reportSize        = 64
	reportID          = 0x04
	commandSetParam   = 0x06
	modeStatic        = 0x06
	brightnessOff     = 0x00
	brightnessHighest = 0x04
)

// buildStaticReport is intentionally byte-for-byte compatible with the v1
// EVision/OpenRGB static-mode report. Do not add initialization commands or
// reinterpret acknowledgement bytes here.
func buildStaticReport(red, green, blue, brightness byte) [reportSize]byte {
	var report [reportSize]byte
	report[0] = reportID
	report[3] = commandSetParam
	report[4] = 8
	report[5] = 0
	report[8] = modeStatic
	report[9] = brightness
	report[10] = 0
	report[11] = 0
	report[12] = 0
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
