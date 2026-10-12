package helps

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image/png"
)

// ValidFilePNG validates full PNG decoding with a bounded 4 megapixel buffer.
func ValidFilePNG(data []byte) bool {
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > 4096 || cfg.Height > 4096 || int64(cfg.Width)*int64(cfg.Height) > 4*1024*1024 {
		return false
	}
	if len(data) < 8 || !bytes.Equal(data[:8], []byte{137, 80, 78, 71, 13, 10, 26, 10}) {
		return false
	}
	offset := 8
	sawData := false
	for offset < len(data) {
		if len(data)-offset < 12 {
			return false
		}
		length := int64(binary.BigEndian.Uint32(data[offset : offset+4]))
		end := int64(offset) + 12 + length
		if end > int64(len(data)) {
			return false
		}
		kind := string(data[offset+4 : offset+8])
		payloadEnd := int(end) - 4
		if crc32.ChecksumIEEE(data[offset+4:payloadEnd]) != binary.BigEndian.Uint32(data[payloadEnd:int(end)]) {
			return false
		}
		if kind == "IDAT" {
			sawData = true
		}
		if kind == "IEND" {
			if length != 0 || !sawData || int(end) != len(data) {
				return false
			}
			_, err := png.Decode(bytes.NewReader(data))
			return err == nil
		}
		offset = int(end)
	}
	return false
}
