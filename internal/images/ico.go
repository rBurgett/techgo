package images

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/png"
	"io"
)

// EncodeICO writes a multi-resolution ICO file to w containing one PNG-encoded
// layer for each size, in the order given. This is the "PNG-in-ICO" form —
// accepted by Vista+ and every modern browser — and is the only way to fit a
// modern, deep-color favicon inside a .ico without the stdlib offering an ICO
// encoder. Each layer is produced by ResizeSquare(src, size). Sizes must be in
// the inclusive range 1..256; 256 is encoded as 0 in the directory entry's
// single width/height bytes, per the ICO format.
func EncodeICO(w io.Writer, src image.Image, sizes ...int) error {
	if len(sizes) == 0 {
		return fmt.Errorf("EncodeICO: at least one size is required")
	}
	for _, sz := range sizes {
		if sz < 1 || sz > 256 {
			return fmt.Errorf("EncodeICO: size %d out of range (1..256)", sz)
		}
	}

	// Encode each PNG layer first so we know its exact byte length, which the
	// directory entries need to reference.
	pngs := make([][]byte, len(sizes))
	for i, sz := range sizes {
		var buf bytes.Buffer
		enc := png.Encoder{CompressionLevel: png.BestCompression}
		if err := enc.Encode(&buf, ResizeSquare(src, sz)); err != nil {
			return fmt.Errorf("encoding PNG layer %dx%d: %w", sz, sz, err)
		}
		pngs[i] = buf.Bytes()
	}

	const (
		dirHeaderLen = 6  // ICONDIR
		dirEntryLen  = 16 // ICONDIRENTRY
	)
	le := binary.LittleEndian
	var out bytes.Buffer
	out.Grow(dirHeaderLen + dirEntryLen*len(sizes) + sumLen(pngs))

	// ICONDIR: Reserved=0, Type=1 (ICO; 2 means CUR), Count=N
	var hdr [dirHeaderLen]byte
	le.PutUint16(hdr[0:2], 0)
	le.PutUint16(hdr[2:4], 1)
	le.PutUint16(hdr[4:6], uint16(len(sizes)))
	out.Write(hdr[:])

	// ICONDIRENTRY × N, in the same order as sizes.
	offset := uint32(dirHeaderLen + dirEntryLen*len(sizes))
	for i, sz := range sizes {
		var e [dirEntryLen]byte
		if sz == 256 {
			e[0], e[1] = 0, 0 // 256 -> 0 (the single-byte width/height fields)
		} else {
			e[0], e[1] = uint8(sz), uint8(sz)
		}
		e[2] = 0                                    // ColorCount: 0 for non-paletted (≥8 bpp)
		e[3] = 0                                    // Reserved: must be 0
		le.PutUint16(e[4:6], 1)                     // Planes: conventionally 1
		le.PutUint16(e[6:8], 32)                    // BitCount: 32 (PNG payload is RGBA)
		le.PutUint32(e[8:12], uint32(len(pngs[i]))) // BytesInRes
		le.PutUint32(e[12:16], offset)              // ImageOffset
		out.Write(e[:])
		offset += uint32(len(pngs[i]))
	}

	// Concatenated PNG payloads, in the same order.
	for _, p := range pngs {
		out.Write(p)
	}

	_, err := out.WriteTo(w)
	return err
}

// SaveICO encodes a PNG-in-ICO file to path (atomically — see writeAtomic),
// containing one layer per size produced by ResizeSquare(src, size). The
// canonical favicon.ico set is 16, 32, 48.
func SaveICO(src image.Image, path string, sizes ...int) error {
	return writeAtomic(path, func(w io.Writer) error {
		return EncodeICO(w, src, sizes...)
	})
}

func sumLen(bs [][]byte) int {
	n := 0
	for _, b := range bs {
		n += len(b)
	}
	return n
}
