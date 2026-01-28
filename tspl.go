package tspl

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"image"

	"github.com/haxii/tspl/bin-img"
)

var DefaultDriver = &Driver{}

type Driver struct{}

type Options struct {
	Peel bool `json:"peel"`
}

var defaultOptions Options

func DefaultOptions() Options {
	return defaultOptions
}

func (t *Driver) Header(w, h, dpm int, opt Options) string {
	dpm = cmp.Or(dpm, 8)
	peel := "OFF"
	if opt.Peel {
		peel = "ON"
	}
	return fmt.Sprintf("SET CUTTER OFF\r\nSET PARTICAL_CUTTER OFF\r\n"+
		"SET PEEL %s\r\nSIZE %.1f mm, %.1f mm\r\nCLS\r\n", peel,
		float64(w)/float64(dpm), float64(h)/float64(dpm))
}

func (t *Driver) Encode(w, h, dpm int, img image.Image, opt Options) ([]byte, error) {
	_, bitmap, err := t.Image2Bytes(img)
	if err != nil {
		return nil, err
	}
	header := t.Header(w, h, dpm, opt)
	tail := "PRINT 1,1\r\n"
	l := len(header) + len(bitmap) + len(tail)
	res := make([]byte, 0, l)
	res = append(res, header...)
	res = append(res, bitmap...)
	res = append(res, tail...)
	return res, nil
}

func (t *Driver) Image2Bytes(img image.Image) (headerSize int, bitmap []byte, err error) {
	var bwImg *bin_img.Binary
	var gErr error
	var isBwImage bool
	if bwImg, isBwImage = img.(*bin_img.Binary); !isBwImage {
		bwImg, gErr = bin_img.FromGrayThreshold(img, 151)
	}
	if gErr != nil {
		err = gErr
		return
	}
	bounds := bwImg.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	rowBytes := (width + 7) / 8

	header := fmt.Sprintf("BITMAP 0,0,%d,%d,1,", rowBytes, height)
	headerSize = len(header)

	bitmap = make([]byte, headerSize+rowBytes*height)

	copy(bitmap[0:], header)

	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if bwImg.IsWhite(x, y) {
				byteIndex := headerSize + y*rowBytes + x/8
				bitIndex := 7 - (x % 8)
				bitmap[byteIndex] |= 1 << bitIndex
			}
		}
	}

	return
}

type Image struct {
	Header []byte
	Bitmap *bin_img.Binary
	Tail   []byte
}

type BitmapHeader struct {
	RowBytes, Width, Height, HeaderEnd int
}

func (t *Driver) ParseBitmapHeader(body []byte) (*BitmapHeader, error) {
	var rowBytes, height, headerEnd int

	if bytes.HasPrefix(body, []byte("BITMAP")) {
		commaCount := 0
		headerEnd = -1
		for i, b := range body {
			if b == ',' {
				commaCount++
				if commaCount == 5 {
					headerEnd = i
					break
				}
			}
		}
		if headerEnd == -1 {
			return nil, errors.New("invalid BITMAP format")
		}
		headerEnd = headerEnd + 1
		var x, y, mode int
		// Example: BITMAP 0,0,90,300,1,
		if _, err := fmt.Sscanf(string(body), "BITMAP %d,%d,%d,%d,%d,",
			&x, &y, &rowBytes, &height, &mode); err != nil {
			return nil, err
		}
	} else {
		return nil, errors.New("not a BITMAP line")
	}
	return &BitmapHeader{
		RowBytes:  rowBytes,
		Width:     rowBytes * 8,
		Height:    height,
		HeaderEnd: headerEnd,
	}, nil
}

// OverlayBinary overlays `overlay` (binary image) onto `base` bitmap raw data without decoding base into an image.
//
// `base` must start with a TSPL `BITMAP ...` header followed by bitmap bytes.
// The overlay is written starting at (xOff,yOff) in pixels (bits). Overlay pixels overwrite base pixels
// (both 0 and 1 are applied).
func (t *Driver) OverlayBinary(baseHeader *BitmapHeader, base []byte, overlay *bin_img.Binary, xOff, yOff int) ([]byte, error) {
	if overlay == nil {
		return nil, errors.New("overlay is nil")
	}
	if xOff < 0 || yOff < 0 {
		return nil, errors.New("xOff/yOff must be >= 0")
	}

	if len(base) == 0 {
		return nil, errors.New("base is empty")
	}
	if baseHeader == nil {
		if bh, err := t.ParseBitmapHeader(base); err != nil {
			return nil, err
		} else {
			baseHeader = bh
		}
	}

	baseRowBytes, baseW, baseH, baseHeaderEnd := baseHeader.RowBytes, baseHeader.Width, baseHeader.Height, baseHeader.HeaderEnd

	// Validate base bitmap byte length.
	baseBitmapLen := baseRowBytes * baseH
	if baseHeaderEnd+baseBitmapLen > len(base) {
		return nil, fmt.Errorf("base body too short: need %d bytes, got %d", baseHeaderEnd+baseBitmapLen, len(base))
	}

	b := overlay.Bounds()
	ovW, ovH := b.Dx(), b.Dy()
	if ovW <= 0 || ovH <= 0 {
		return nil, fmt.Errorf("invalid overlay size: %dx%d", ovW, ovH)
	}

	// Ensure overlay fits within base.
	if xOff+ovW > baseW || yOff+ovH > baseH {
		return nil, fmt.Errorf("overlay out of bounds: base=%dx%d, overlay=%dx%d, off=(%d,%d)", baseW, baseH, ovW, ovH, xOff, yOff)
	}

	// Copy base to result and patch only bitmap region.
	res := make([]byte, len(base))
	copy(res, base)

	// Bit-accurate overlay (works for any xOff alignment).
	for y := 0; y < ovH; y++ {
		for x := 0; x < ovW; x++ {
			// Keep consistent with Image2Bytes(): IsWhite -> bit 1
			var bit uint8
			if overlay.IsWhite(x+b.Min.X, y+b.Min.Y) {
				bit = 1
			} else {
				bit = 0
			}
			setBitmapBit(res, baseHeaderEnd, baseRowBytes, x+xOff, y+yOff, bit)
		}
	}

	return res, nil
}

// OverlayBinaryAtTopLeft overlays `overlay` onto `base` starting at (0,0).
func (t *Driver) OverlayBinaryAtTopLeft(baseHeader *BitmapHeader, base []byte, overlay *bin_img.Binary) ([]byte, error) {
	return t.OverlayBinary(baseHeader, base, overlay, 0, 0)
}

func setBitmapBit(body []byte, headerEnd, rowBytes, x, y int, bit uint8) {
	byteIndex := headerEnd + y*rowBytes + x/8
	bitIndex := 7 - (x % 8)
	mask := byte(1 << bitIndex)
	if bit == 0 {
		body[byteIndex] &^= mask
		return
	}
	body[byteIndex] |= mask
}

func (t *Driver) Bytes2Image(body []byte) (*Image, error) {
	h, err := t.ParseBitmapHeader(body)
	if err != nil {
		return nil, err
	}
	// Find BITMAP line
	rowBytes, width, height, headerEnd := h.RowBytes, h.Width, h.Height, h.HeaderEnd

	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("invalid image size got width: %d, height: %d", width, height)
	}
	img, err := bin_img.NewBinary(width, height)
	if err != nil {
		return nil, err
	}

	bodyEnd := headerEnd
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			byteIndex := headerEnd + y*rowBytes + x/8
			if byteIndex >= len(body) {
				continue
			}
			bitIndex := 7 - (x % 8)
			bodyEnd = byteIndex
			if (body[byteIndex]>>bitIndex)&1 == 0 {
				img.SetOff(x, y)
			} else {
				img.SetOn(x, y)
			}
		}
	}

	return &Image{
		Header: body[:headerEnd],
		Bitmap: img,
		Tail:   body[bodyEnd+1:],
	}, nil
}
