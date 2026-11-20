// Command icon renders the application mark in the formats used by each desktop.
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
)

type point struct{ x, y float64 }

func distance(p, a, b point) float64 {
	dx, dy := b.x-a.x, b.y-a.y
	offset := math.Max(0, math.Min(1, ((p.x-a.x)*dx+(p.y-a.y)*dy)/(dx*dx+dy*dy)))
	return math.Hypot(p.x-a.x-offset*dx, p.y-a.y-offset*dy)
}
func mark(p point) color.NRGBA {
	x, y := math.Abs(p.x-0.5), math.Abs(p.y-0.5)
	if math.Hypot(math.Max(x-0.25, 0), math.Max(y-0.25, 0)) > .205 {
		return color.NRGBA{}
	}
	t := 0.64*p.y + 0.36*p.x
	c := color.NRGBA{uint8(65 - 30*t), uint8(121 - 46*t), uint8(238 - 48*t), 255}
	edges := [][2]point{
		{{.5, .255}, {.5, .586}}, {{.361, .465}, {.5, .603}}, {{.5, .603}, {.639, .465}},
		{{.278, .646}, {.278, .738}}, {{.278, .738}, {.722, .738}}, {{.722, .738}, {.722, .646}},
	}
	for _, edge := range edges {
		if distance(p, edge[0], edge[1]) <= .032 {
			return color.NRGBA{255, 255, 255, 255}
		}
	}
	return c
}
func render(size int) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var r, g, b, a, n int
			for j := 0; j < 4; j++ {
				for i := 0; i < 4; i++ {
					c := mark(point{(float64(x) + (float64(i)+.5)/4) / float64(size), (float64(y) + (float64(j)+.5)/4) / float64(size)})
					if c.A == 0 {
						continue
					}
					r += int(c.R)
					g += int(c.G)
					b += int(c.B)
					a += int(c.A)
					n++
				}
			}
			if n > 0 {
				img.SetNRGBA(x, y, color.NRGBA{uint8(r / n), uint8(g / n), uint8(b / n), uint8(a / 16)})
			}
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		log.Fatal(err)
	}
	return out.Bytes()
}
func write(path string, data []byte) {
	if err := os.WriteFile(path, data, 0644); err != nil {
		log.Fatal(err)
	}
}
func main() {
	sizes := []int{16, 32, 48, 64, 128, 256, 512, 1024}
	images := map[int][]byte{}
	for _, n := range sizes {
		images[n] = render(n)
	}
	if err := os.MkdirAll("desktop/assets", 0755); err != nil {
		log.Fatal(err)
	}
	write("web/app-icon.png", images[128])
	write("desktop/assets/icon.png", images[512])
	var icns, body bytes.Buffer
	for _, entry := range []struct {
		code string
		size int
	}{{"icp4", 16}, {"icp5", 32}, {"icp6", 64}, {"ic07", 128}, {"ic08", 256}, {"ic09", 512}, {"ic10", 1024}} {
		data := images[entry.size]
		body.WriteString(entry.code)
		binary.Write(&body, binary.BigEndian, uint32(len(data)+8))
		body.Write(data)
	}
	icns.WriteString("icns")
	binary.Write(&icns, binary.BigEndian, uint32(body.Len()+8))
	icns.Write(body.Bytes())
	write("desktop/assets/icon.icns", icns.Bytes())
	icoSizes := sizes[:6]
	var ico bytes.Buffer
	for _, v := range []uint16{0, 1, uint16(len(icoSizes))} {
		binary.Write(&ico, binary.LittleEndian, v)
	}
	offset := 6 + len(icoSizes)*16
	for _, n := range icoSizes {
		ico.Write([]byte{byte(n % 256), byte(n % 256), 0, 0, 1, 0, 32, 0})
		binary.Write(&ico, binary.LittleEndian, uint32(len(images[n])))
		binary.Write(&ico, binary.LittleEndian, uint32(offset))
		offset += len(images[n])
	}
	for _, n := range icoSizes {
		ico.Write(images[n])
	}
	write("desktop/assets/icon.ico", ico.Bytes())
}
