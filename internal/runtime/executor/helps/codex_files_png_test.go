package helps

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/png"
	"testing"
)

func TestFilePNGRejectsCorruptIDATWithValidCRC(t *testing.T) {
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	data := b.Bytes()
	if !ValidFilePNG(data) {
		t.Fatal("valid png")
	}
	for offset := 8; offset < len(data); {
		n := int(binary.BigEndian.Uint32(data[offset : offset+4]))
		if string(data[offset+4:offset+8]) == "IDAT" {
			data[offset+8] ^= 0xff
			binary.BigEndian.PutUint32(data[offset+8+n:offset+12+n], crc32.ChecksumIEEE(data[offset+4:offset+8+n]))
			break
		}
		offset += 12 + n
	}
	if ValidFilePNG(data) {
		t.Fatal("invalid zlib accepted")
	}
}
