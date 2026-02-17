package main

import (
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

const (
	grayR    = 0.299
	grayG    = 0.587
	grayB    = 0.114
	maxUint8 = 255
)

// PixelAccessor provides fast direct pixel access for image types
type PixelAccessor struct {
	pix    []uint8
	stride int
	rect   image.Rectangle
}

func newPixelAccessor(img *image.Gray) *PixelAccessor {
	return &PixelAccessor{
		pix:    img.Pix,
		stride: img.Stride,
		rect:   img.Rect,
	}
}

func (pa *PixelAccessor) at(x, y int) uint8 {
	idx := (y-pa.rect.Min.Y)*pa.stride + (x - pa.rect.Min.X)
	return pa.pix[idx]
}

func (pa *PixelAccessor) set(x, y int, v uint8) {
	idx := (y-pa.rect.Min.Y)*pa.stride + (x - pa.rect.Min.X)
	pa.pix[idx] = v
}

func resizeImage(img image.Image, targetWidth, targetHeight int) image.Image {
	srcBounds := img.Bounds()
	srcWidth := srcBounds.Dx()
	srcHeight := srcBounds.Dy()

	dst := image.NewRGBA(image.Rect(0, 0, targetWidth, targetHeight))

	// Pre-calculate X mappings to avoid重复 computation
	xMap := make([]int, targetWidth)
	for x := range targetWidth {
		xMap[x] = x*srcWidth/targetWidth + srcBounds.Min.X
	}

	for y := range targetHeight {
		srcY := y*srcHeight/targetHeight + srcBounds.Min.Y
		for x := range targetWidth {
			dst.Set(x, y, img.At(xMap[x], srcY))
		}
	}

	return dst
}

func clearScreen() {
	fmt.Print("\033[H\033[2J")
}

func renderProgressBar(current, total, width int) {
	percent := float64(current) / float64(total)
	filled := int(percent * float64(width))
	bar := "[" + strings.Repeat("=", filled) + strings.Repeat(" ", width-filled) + "]"
	fmt.Printf("\rProcessing video %s %d/%d", bar, current, total)
}

func processFramesFromFolder(folderPath string) []image.Image {
	files, err := os.ReadDir(folderPath)
	if err != nil {
		panic(err)
	}

	// Filter only image files
	var imageFiles []os.DirEntry
	for _, file := range files {
		if file.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(file.Name()))
		if ext == ".jpg" || ext == ".jpeg" || ext == ".png" {
			imageFiles = append(imageFiles, file)
		}
	}

	total := len(imageFiles)
	images := make([]image.Image, total)

	// Parallel processing with worker pool
	numWorkers := runtime.NumCPU()
	if numWorkers > total {
		numWorkers = total
	}

	jobs := make(chan int, total)
	var wg sync.WaitGroup

	// Start workers
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				file := imageFiles[idx]
				path := filepath.Join(folderPath, file.Name())
				f, err := os.Open(path)
				if err != nil {
					continue
				}
				img, _, err := image.Decode(f)
				f.Close()
				if err != nil {
					continue
				}
				images[idx] = img
			}
		}()
	}

	// Send jobs
	for i := range total {
		jobs <- i
		renderProgressBar(i+1, total, 40)
	}
	close(jobs)
	wg.Wait()

	// Remove nil entries (failed loads)
	var result []image.Image
	for _, img := range images {
		if img != nil {
			result = append(result, img)
		}
	}

	return result
}

func processImage(img image.Image) image.Image {
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	// Convert to grayscale
	grayImg := image.NewGray(bounds)
	draw.Draw(grayImg, grayImg.Bounds(), img, img.Bounds().Min, draw.Src)

	// Single-pass contrast enhancement with min/max tracking
	pa := newPixelAccessor(grayImg)

	var minGray, maxGray uint8 = maxUint8, 0

	// Find min/max in one pass
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			g := pa.at(x, y)
			if g < minGray {
				minGray = g
			}
			if g > maxGray {
				maxGray = g
			}
		}
	}

	// Create output image
	edgeImg := image.NewGray(bounds)

	if minGray == maxGray {
		// Flat image - return as-is
		return edgeImg
	}

	// Apply contrast enhancement and Sobel in fewer passes
	contrastImg := image.NewGray(bounds)
	contrastPA := newPixelAccessor(contrastImg)

	scaleFactor := float64(maxUint8) / float64(maxGray-minGray)

	// Single pass: contrast enhancement
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			originalGray := pa.at(x, y)
			stretchedGray := uint8(float64(originalGray-minGray) * scaleFactor)
			contrastPA.set(x, y, stretchedGray)
		}
	}

	// Optimized Sobel with single-pass normalization
	edgePA := newPixelAccessor(edgeImg)

	sobelX := [3][3]int{{-1, 0, 1}, {-2, 0, 2}, {-1, 0, 1}}
	sobelY := [3][3]int{{-1, -2, -1}, {0, 0, 0}, {1, 2, 1}}

	// Use flat slice for better cache locality
	magnitudes := make([]float64, (width-2)*(height-2))
	maxMagnitude := 0.0

	idx := 0
	for y := bounds.Min.Y + 1; y < bounds.Max.Y-1; y++ {
		for x := bounds.Min.X + 1; x < bounds.Max.X-1; x++ {
			var gx, gy float64

			// Unrolled kernel loops for better performance
			for ky := -1; ky <= 1; ky++ {
				for kx := -1; kx <= 1; kx++ {
					pixelVal := float64(contrastPA.at(x+kx, y+ky))
					gx += pixelVal * float64(sobelX[ky+1][kx+1])
					gy += pixelVal * float64(sobelY[ky+1][kx+1])
				}
			}

			magnitude := math.Sqrt(gx*gx + gy*gy)
			magnitudes[idx] = magnitude
			if magnitude > maxMagnitude {
				maxMagnitude = magnitude
			}
			idx++
		}
	}

	// Normalize and write in single pass
	if maxMagnitude > 0 {
		normalizeScale := maxUint8 / maxMagnitude
		idx = 0
		for y := bounds.Min.Y + 1; y < bounds.Max.Y-1; y++ {
			for x := bounds.Min.X + 1; x < bounds.Max.X-1; x++ {
				normalizedVal := magnitudes[idx] * normalizeScale
				if normalizedVal > maxUint8 {
					normalizedVal = maxUint8
				}
				edgePA.set(x, y, uint8(normalizedVal))
				idx++
			}
		}
	}

	return edgeImg
}

func printAscii(img image.Image, width, height int) {
	// Better character ramp with more shades for smoother gradients
	// From darkest to lightest: solid block -> detailed characters -> space
	darkToLight := "@%#*+=-:. "
	numCharsInRamp := len(darkToLight)

	bounds := img.Bounds()
	imgWidth := bounds.Dx()
	imgHeight := bounds.Dy()

	// Account for terminal character aspect ratio (characters are ~2x taller than wide)
	// This prevents the image from looking stretched vertically
	charAspectRatio := 0.5
	aspectRatio := float64(imgWidth) / float64(imgHeight) * charAspectRatio
	newWidth := width
	newHeight := int(float64(newWidth) / aspectRatio)

	if newHeight > height {
		newHeight = height
		newWidth = int(float64(newHeight) * aspectRatio)
	}

	resizedImg := resizeImage(img, newWidth, newHeight)
	processedImg := processImage(resizedImg)

	bounds = processedImg.Bounds()

	// Pre-allocate strings.Builder for efficient string concatenation
	var sb strings.Builder
	sb.Grow(newWidth*newHeight + newHeight) // Approximate size

	// Cache pixel values for processedImg if it's *image.Gray
	var grayImg *image.Gray
	var pix []uint8
	var stride int

	if g, ok := processedImg.(*image.Gray); ok {
		grayImg = g
		pix = g.Pix
		stride = g.Stride
	}

	charScale := float64(numCharsInRamp) / 256.0
	// Gamma correction for perceptual uniformity
	gamma := 0.8

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			var gray uint8

			if grayImg != nil {
				// Fast path: direct pixel access
				idx := (y-bounds.Min.Y)*stride + (x - bounds.Min.X)
				gray = pix[idx]
			} else {
				// Slow path: use At() for other image types
				r, g, b, _ := processedImg.At(x, y).RGBA()
				gray = uint8(grayR*float64(uint8(r>>8)) +
					grayG*float64(uint8(g>>8)) +
					grayB*float64(uint8(b>>8)))
			}

			// Apply gamma correction for better perceptual gradation
			normalizedGray := float64(gray) / 255.0
			correctedGray := uint8(math.Pow(normalizedGray, gamma) * 255.0)

			characterIndex := int(float64(correctedGray) * charScale)
			if characterIndex >= numCharsInRamp {
				characterIndex = numCharsInRamp - 1
			}

			sb.WriteByte(darkToLight[characterIndex])
		}
		sb.WriteByte('\n')
	}
	fmt.Print(sb.String())
}

func main() {
	if !term.IsTerminal(0) {
		fmt.Println("Not a terminal")
		return
	}

	width, height, err := term.GetSize(0)
	if err != nil {
		panic(err)
	}
	fmt.Printf("Width: %d, Height: %d\n", width, height)

	file, err := os.Open("./frames")
	if err != nil {
		panic(err)
	}
	defer file.Close()

	fileInfo, err := file.Stat()
	if err != nil {
		panic(err)
	}

	ext := strings.ToLower(filepath.Ext(fileInfo.Name()))
	var img image.Image
	var images []image.Image

	switch ext {
	case ".jpg", ".jpeg":
		img, err = jpeg.Decode(file)
		if err != nil {
			panic(err)
		}
		images = append(images, img)
		fmt.Println("It's a JPEG")
	case ".png":
		img, err = png.Decode(file)
		if err != nil {
			panic(err)
		}
		images = append(images, img)
		fmt.Println("It's a PNG")
	default:
		folderPath := "./frames/"
		images = processFramesFromFolder(folderPath)
		fmt.Println() // New line after progress bar
	}

	frameRate := 50
	frameDelay := time.Duration(1000.0/frameRate) * time.Millisecond

	for _, img := range images {
		clearScreen()
		printAscii(img, width, height)
		time.Sleep(frameDelay)
	}
}
