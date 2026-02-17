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

	// If flat image, return grayscale as-is
	if minGray == maxGray {
		return grayImg
	}

	// Apply contrast enhancement for better ASCII representation
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

	return contrastImg
}

func printAscii(img image.Image, width, height int) {
	// Better character ramp with more shades for smoother gradients
	// From darkest to lightest: solid block -> detailed characters -> space
	darkToLight := "@#%*+=-:. "
	numCharsInRamp := len(darkToLight)

	bounds := img.Bounds()
	imgWidth := bounds.Dx()
	imgHeight := bounds.Dy()

	// Calculate target dimensions to fill terminal while preserving aspect ratio
	// Terminal chars are ~2x taller than wide, so we compensate
	charAspectRatio := 2.0 // height/width of a terminal character
	imageAspectRatio := float64(imgWidth) / float64(imgHeight)

	// Scale image to fit within terminal, accounting for character aspect ratio
	scaledAspectRatio := imageAspectRatio / charAspectRatio

	newWidth := width
	newHeight := int(float64(newWidth) / scaledAspectRatio)

	if newHeight > height {
		newHeight = height
		newWidth = int(float64(newHeight) * scaledAspectRatio)
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
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Println("Not a terminal")
		return
	}

	width, height, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		panic(err)
	}
	// Reserve 1 line for status/debug output
	height = height - 1
	fmt.Printf("Terminal: %dx%d\n", width, height)

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
