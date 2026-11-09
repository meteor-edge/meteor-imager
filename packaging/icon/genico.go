//go:build ignore

// genico writes icon.ico (16–256 px PNG entries) from icon.png:
//
//	go run packaging/icon/genico.go packaging/icon/icon.png packaging/icon/icon.ico
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"os"
)

// scale is an area-averaging downscale, good enough for icons.
func scale(src image.Image, size int) image.Image {
	b := src.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))
	fx, fy := float64(b.Dx())/float64(size), float64(b.Dy())/float64(size)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var r, g, bl, a, n float64
			for sy := int(float64(y) * fy); sy < int(float64(y+1)*fy); sy++ {
				for sx := int(float64(x) * fx); sx < int(float64(x+1)*fx); sx++ {
					c := color.NRGBAModel.Convert(src.At(b.Min.X+sx, b.Min.Y+sy)).(color.NRGBA)
					w := float64(c.A)
					r, g, bl, a, n = r+float64(c.R)*w, g+float64(c.G)*w, bl+float64(c.B)*w, a+w, n+1
				}
			}
			if a > 0 {
				dst.Set(x, y, color.NRGBA{uint8(r / a), uint8(g / a), uint8(bl / a), uint8(a / n)})
			}
		}
	}
	return dst
}

func main() {
	f, err := os.Open(os.Args[1])
	if err != nil {
		panic(err)
	}
	src, err := png.Decode(f)
	if err != nil {
		panic(err)
	}
	sizes := []int{256, 48, 32, 16}
	var images [][]byte
	for _, s := range sizes {
		var buf bytes.Buffer
		png.Encode(&buf, scale(src, s))
		images = append(images, buf.Bytes())
	}
	var out bytes.Buffer
	binary.Write(&out, binary.LittleEndian, [3]uint16{0, 1, uint16(len(sizes))})
	offset := 6 + 16*len(sizes)
	for i, s := range sizes {
		dim := uint8(s)
		if s == 256 {
			dim = 0
		}
		binary.Write(&out, binary.LittleEndian, struct {
			W, H, Colors, Reserved uint8
			Planes, Bits           uint16
			Size, Offset           uint32
		}{dim, dim, 0, 0, 1, 32, uint32(len(images[i])), uint32(offset)})
		offset += len(images[i])
	}
	for _, img := range images {
		out.Write(img)
	}
	if err := os.WriteFile(os.Args[2], out.Bytes(), 0o644); err != nil {
		panic(err)
	}
}
