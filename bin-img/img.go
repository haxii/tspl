package bin_img

import (
	"errors"
	"image"
	"image/color"
	"math/bits"
)

// Binary is a 1bpp (bit-packed) image: 8 pixels per byte, MSB first.
// A bit value of 1 means "on" (white = 255), 0 means "off" (black = 0).
type Binary struct {
	// Pix holds packed pixels, row-major. Each row is Stride bytes.
	Pix    []byte
	Stride int // bytes per row
	Rect   image.Rectangle
}

// NewBinary creates a 1bpp image with the given width and height.
// For simplicity and speed, width MUST be a multiple of 8.
func NewBinary(w, h int) (*Binary, error) {
	if w <= 0 || h <= 0 {
		return nil, errors.New("binimg: invalid dimensions")
	}
	if w%8 != 0 {
		return nil, errors.New("binimg: width must be a multiple of 8")
	}
	stride := w / 8
	pix := make([]byte, stride*h)
	return &Binary{
		Pix:    pix,
		Stride: stride,
		Rect:   image.Rect(0, 0, w, h),
	}, nil
}

// Bounds implements image.Image.
func (b *Binary) Bounds() image.Rectangle { return b.Rect }

// BinaryModel implements image.Image.
// We map bits to gray: 0 -> Gray{0}, 1 -> Gray{255}.
var BinaryModel color.Model = color.ModelFunc(func(c color.Color) color.Color {
	// Threshold at mid-gray. Alpha==0 -> off.
	r, g, bl, a := c.RGBA()
	if a == 0 {
		return color.Gray{0}
	}
	// Luma-ish threshold on 16-bit range.
	// (299, 587, 114) are standard coefficients scaled by 1000.
	y := (299*r + 587*g + 114*bl) / 1000
	if y >= 0x8000 {
		return color.Gray{255}
	}
	return color.Gray{0}
})

func (b *Binary) ColorModel() color.Model { return BinaryModel }

// At implements image.Image. Returns Gray{0} or Gray{255}.
func (b *Binary) At(x, y int) color.Color {
	if !image.Pt(x, y).In(b.Rect) {
		return color.Gray{0}
	}
	if b.bit(x, y) {
		return color.Gray{255}
	}
	return color.Gray{0}
}

func (b *Binary) IsWhite(x, y int) bool {
	return b.bit(x, y)
}

func (b *Binary) IsBlack(x, y int) bool {
	return !b.bit(x, y)
}

// Set implements draw.Image (thresholds incoming color to on/off).
func (b *Binary) Set(x, y int, c color.Color) {
	if !image.Pt(x, y).In(b.Rect) {
		return
	}
	c = BinaryModel.Convert(c)
	g := c.(color.Gray)
	b.setBit(x, y, g.Y >= 128)
}

// Opaque implements the Opaque method some encoders check.
func (b *Binary) Opaque() bool { return true }

// SubImage implements the usual view into the same backing store.
func (b *Binary) SubImage(r image.Rectangle) image.Image {
	r = r.Intersect(b.Rect)
	if r.Empty() {
		return &Binary{Rect: r}
	}
	// Byte offset of top-left corner
	off := b.pixOffset(r.Min.X, r.Min.Y)
	return &Binary{
		Pix:    b.Pix[off:],
		Stride: b.Stride,
		Rect:   r,
	}
}

// -------- Helpers --------

func (b *Binary) pixOffset(x, y int) int {
	return (y-b.Rect.Min.Y)*b.Stride + (x-b.Rect.Min.X)/8
}

func (b *Binary) bit(x, y int) bool {
	i := b.pixOffset(x, y)
	mask := byte(0x80 >> (uint(x) & 7)) // MSB-first
	return (b.Pix[i] & mask) != 0
}

func (b *Binary) setBit(x, y int, on bool) {
	i := b.pixOffset(x, y)
	mask := byte(0x80 >> (uint(x) & 7))
	if on {
		b.Pix[i] |= mask
	} else {
		b.Pix[i] &^= mask
	}
}

func (b *Binary) SetOn(x, y int)  { b.setBit(x, y, true) }
func (b *Binary) SetOff(x, y int) { b.setBit(x, y, false) }

// Fill sets all pixels to on or off.
func (b *Binary) Fill(on bool) {
	fill := byte(0x00)
	if on {
		fill = 0xFF
	}
	for i := range b.Pix {
		b.Pix[i] = fill
	}
}

func FromGrayThreshold(src image.Image, thresh uint8) (*Binary, error) {
	b, err := NewBinary(src.Bounds().Dx(), src.Bounds().Dy())
	if err != nil {
		return nil, err
	}
	b.FromGrayThreshold(src, thresh)
	return b, nil
}

// FromGrayThreshold writes into b from a source grayscale/rgba image,
// using the given 0..255 threshold (>= thresh => on).
func (b *Binary) FromGrayThreshold(src image.Image, thresh uint8) {
	bounds := b.Rect
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		row := b.Pix[(y-bounds.Min.Y)*b.Stride : (y-bounds.Min.Y+1)*b.Stride]
		// zero the row first
		for i := range row {
			row[i] = 0
		}
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, bl, a := src.At(x, y).RGBA()
			var on bool
			if a == 0 {
				on = false
			} else {
				// 8-bit luma-ish
				y8 := uint8(((299*r + 587*g + 114*bl) / 1000) >> 8)
				on = y8 >= thresh
			}
			if on {
				i := (x - bounds.Min.X) >> 3
				bit := byte(0x80 >> (uint(x) & 7))
				row[i] |= bit
			}
		}
	}
}

// BytesPerPixel is 0.125 for convenience (as a fraction).
func (b *Binary) BytesPerPixel() float64 { return 0.125 }

// MemoryUsage returns approximate bytes for pixel storage.
func (b *Binary) MemoryUsage() int { return len(b.Pix) }

// Rotate180 rotates the 1bpp image by 180 degrees in-place.
//
// Fast path requirements:
//   - width is a multiple of 8 (true for NewBinary)
//   - Rect.Min.X is byte-aligned (multiple of 8)
//
// The fast path runs in O(H*W/8) time by swapping bytes and reversing bits.
func (b *Binary) Rotate180() {
	w, h := b.Rect.Dx(), b.Rect.Dy()
	if w <= 1 && h <= 1 {
		return
	}

	// Number of bytes that represent a visible row for this Binary view.
	rowBytes := (w + 7) >> 3
	if rowBytes <= 0 {
		return
	}

	// If this view is not byte-aligned or width isn't byte-aligned, fall back.
	// (NewBinary always satisfies these; SubImage may not.)
	if (b.Rect.Min.X&7) != 0 || (w&7) != 0 {
		// Correct but slower: swap individual bits.
		minX, minY := b.Rect.Min.X, b.Rect.Min.Y
		for y := 0; y < h/2; y++ {
			y1 := minY + y
			y2 := minY + (h - 1 - y)
			for x := 0; x < w; x++ {
				x1 := minX + x
				x2 := minX + (w - 1 - x)
				b1 := b.bit(x1, y1)
				b2 := b.bit(x2, y2)
				b.setBit(x1, y1, b2)
				b.setBit(x2, y2, b1)
			}
		}
		if h%2 == 1 {
			y := minY + h/2
			for x := 0; x < w/2; x++ {
				x1 := minX + x
				x2 := minX + (w - 1 - x)
				b1 := b.bit(x1, y)
				b2 := b.bit(x2, y)
				b.setBit(x1, y, b2)
				b.setBit(x2, y, b1)
			}
		}
		return
	}

	// Fast path: swap rows and reverse each row's bits by reversing byte order
	// and applying Reverse8 to each byte.
	for y := 0; y < h/2; y++ {
		off1 := y * b.Stride
		off2 := (h - 1 - y) * b.Stride
		row1 := b.Pix[off1 : off1+rowBytes]
		row2 := b.Pix[off2 : off2+rowBytes]
		for i := 0; i < rowBytes; i++ {
			j := rowBytes - 1 - i
			a := row1[i]
			c := row2[j]
			row1[i] = byte(bits.Reverse8(uint8(c)))
			row2[j] = byte(bits.Reverse8(uint8(a)))
		}
	}

	// Middle row (if any): reverse it in-place.
	if h%2 == 1 {
		off := (h / 2) * b.Stride
		row := b.Pix[off : off+rowBytes]
		for i := 0; i < rowBytes/2; i++ {
			j := rowBytes - 1 - i
			a := row[i]
			row[i] = byte(bits.Reverse8(uint8(row[j])))
			row[j] = byte(bits.Reverse8(uint8(a)))
		}
		if rowBytes%2 == 1 {
			mid := rowBytes / 2
			row[mid] = byte(bits.Reverse8(uint8(row[mid])))
		}
	}
}

// -------- Optional utilities --------

// ToPaletted makes a temporary 2-color paletted image (useful for PNG encoding).
// Note: this allocates 1 byte per pixel (only for the exported image),
// not for your in-memory working buffer.
func (b *Binary) ToPaletted() *image.Paletted {
	p := image.NewPaletted(b.Rect, color.Palette{
		color.Gray{0}, color.Gray{255},
	})
	bounds := b.Rect
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if b.bit(x, y) {
				p.SetColorIndex(x, y, 1)
			} else {
				p.SetColorIndex(x, y, 0)
			}
		}
	}
	return p
}
