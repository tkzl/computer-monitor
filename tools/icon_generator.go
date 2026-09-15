//go:build ignore

// 在项目根目录运行 go run ./tools/icon_generator.go，生成 Windows 多尺寸应用图标。
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"runtime"
)

var (
	iconBG   = [3]float64{0x06 / 255.0, 0x11 / 255.0, 0x20 / 255.0}
	iconRing = [3]float64{0x00 / 255.0, 0xE5 / 255.0, 0xFF / 255.0}
	iconArc  = [3]float64{0xFF / 255.0, 0x2D / 255.0, 0x95 / 255.0}
)

func sample(x, y float64) ([3]float64, float64) {
	dx, dy := x-54, y-54
	distance := math.Hypot(dx, dy)
	if distance > 54 {
		return iconBG, 0
	}
	angle := math.Atan2(dy, dx)
	var arcDistance float64
	if angle >= -math.Pi/2 && angle <= 0 {
		arcDistance = math.Abs(distance - 30)
	} else {
		arcDistance = math.Min(math.Hypot(dx, dy+30), math.Hypot(dx-30, dy))
	}
	if arcDistance <= 3 {
		return iconArc, 1
	}
	if math.Abs(distance-30) <= 2.5 || distance <= 8 {
		return iconRing, 1
	}
	return iconBG, 1
}

func render(size int) *image.RGBA {
	const supersample = 4
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	scale := float64(size*supersample) / 108
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var red, green, blue, alpha float64
			for sy := 0; sy < supersample; sy++ {
				for sx := 0; sx < supersample; sx++ {
					x := (float64(px*supersample+sx) + 0.5) / scale
					y := (float64(py*supersample+sy) + 0.5) / scale
					value, a := sample(x, y)
					red += value[0] * a
					green += value[1] * a
					blue += value[2] * a
					alpha += a
				}
			}
			a := alpha / float64(supersample*supersample)
			pixel := color.RGBA{A: uint8(a * 255)}
			if alpha > 0 {
				pixel.R = uint8(red / alpha * 255)
				pixel.G = uint8(green / alpha * 255)
				pixel.B = uint8(blue / alpha * 255)
			}
			img.SetRGBA(px, py, pixel)
		}
	}
	return img
}

func encode(images ...*image.RGBA) []byte {
	type entry struct {
		width, height int
		data          []byte
	}
	entries := make([]entry, 0, len(images))
	for _, img := range images {
		width, height := img.Bounds().Dx(), img.Bounds().Dy()
		pixels := make([]byte, width*height*4)
		pos := 0
		for y := height - 1; y >= 0; y-- {
			for x := 0; x < width; x++ {
				pixel := img.RGBAAt(x, y)
				pixels[pos], pixels[pos+1], pixels[pos+2], pixels[pos+3] = pixel.B, pixel.G, pixel.R, pixel.A
				pos += 4
			}
		}
		mask := make([]byte, ((width+31)/32)*4*height)
		var dib bytes.Buffer
		binary.Write(&dib, binary.LittleEndian, int32(40))
		binary.Write(&dib, binary.LittleEndian, int32(width))
		binary.Write(&dib, binary.LittleEndian, int32(height*2))
		binary.Write(&dib, binary.LittleEndian, uint16(1))
		binary.Write(&dib, binary.LittleEndian, uint16(32))
		binary.Write(&dib, binary.LittleEndian, uint32(0))
		binary.Write(&dib, binary.LittleEndian, uint32(len(pixels)+len(mask)))
		for range 4 {
			binary.Write(&dib, binary.LittleEndian, uint32(0))
		}
		dib.Write(pixels)
		dib.Write(mask)
		entries = append(entries, entry{width: width, height: height, data: dib.Bytes()})
	}

	var out bytes.Buffer
	binary.Write(&out, binary.LittleEndian, uint16(0))
	binary.Write(&out, binary.LittleEndian, uint16(1))
	binary.Write(&out, binary.LittleEndian, uint16(len(entries)))
	offset := 6 + 16*len(entries)
	for _, item := range entries {
		width, height := byte(item.width), byte(item.height)
		if item.width >= 256 {
			width = 0
		}
		if item.height >= 256 {
			height = 0
		}
		out.WriteByte(width)
		out.WriteByte(height)
		out.Write([]byte{0, 0})
		binary.Write(&out, binary.LittleEndian, uint16(1))
		binary.Write(&out, binary.LittleEndian, uint16(32))
		binary.Write(&out, binary.LittleEndian, uint32(len(item.data)))
		binary.Write(&out, binary.LittleEndian, uint32(offset))
		offset += len(item.data)
	}
	for _, item := range entries {
		out.Write(item.data)
	}
	return out.Bytes()
}

func main() {
	sizes := []int{16, 24, 32, 48, 64, 128, 256}
	images := make([]*image.RGBA, 0, len(sizes))
	for _, size := range sizes {
		images = append(images, render(size))
	}
	_, sourceFile, _, _ := runtime.Caller(0)
	projectRoot := filepath.Dir(filepath.Dir(sourceFile))
	assetsDir := filepath.Join(projectRoot, "assets")
	if err := os.MkdirAll(assetsDir, 0o755); err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(assetsDir, "monitor.ico"), encode(images...), 0o644); err != nil {
		panic(err)
	}
}
