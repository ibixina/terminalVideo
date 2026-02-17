package main

import (
	"flag"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
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

// extractFramesFromVideo extracts frames from a video using ffmpeg
// Returns the temp directory path and starts extraction in background
func extractFramesFromVideo(videoPath string) (string, error) {
	// Create temporary directory for frames
	tempDir, err := os.MkdirTemp("", "terminalvideo-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temp directory: %w", err)
	}

	// Extract frames at 30fps using ffmpeg
	framePattern := filepath.Join(tempDir, "frame_%05d.jpg")

	fmt.Printf("Starting extraction from %s...\n", videoPath)

	cmd := exec.Command("ffmpeg",
		"-i", videoPath,
		"-vf", "fps=30,scale=480:-1:flags=lanczos",
		"-q:v", "2",
		framePattern,
	)

	// Start ffmpeg in background
	if err := cmd.Start(); err != nil {
		os.RemoveAll(tempDir)
		return "", fmt.Errorf("failed to start ffmpeg: %w", err)
	}

	// Store cmd in temp file so we can check when it's done
	// Actually, let's just let it run and monitor files
	go func() {
		cmd.Wait()
	}()

	return tempDir, nil
}

// streamVideoFrames streams frames from a video file as they're extracted
// Returns a channel that yields images and a done channel
func streamVideoFrames(videoPath string) (<-chan image.Image, chan error) {
	imgChan := make(chan image.Image, 30) // Buffer 1 second of frames
	errChan := make(chan error, 1)

	go func() {
		defer close(imgChan)
		defer close(errChan)

		// Create temp directory
		tempDir, err := os.MkdirTemp("", "terminalvideo-*")
		if err != nil {
			errChan <- fmt.Errorf("failed to create temp directory: %w", err)
			return
		}
		defer os.RemoveAll(tempDir)

		// Start ffmpeg extracting frames
		framePattern := filepath.Join(tempDir, "frame_%05d.jpg")
		cmd := exec.Command("ffmpeg",
			"-i", videoPath,
			"-vf", "fps=30,scale=1280:-1:flags=lanczos",
			"-q:v", "2",
			framePattern,
		)

		if err := cmd.Start(); err != nil {
			errChan <- fmt.Errorf("failed to start ffmpeg: %w", err)
			return
		}

		// Wait for first frame to appear, then start streaming
		frameNum := 1
		consecutiveEmpty := 0
		maxConsecutiveEmpty := 10 // Stop after 10 empty checks after ffmpeg exits

		for {
			framePath := filepath.Join(tempDir, fmt.Sprintf("frame_%05d.jpg", frameNum))

			// Try to load the frame
			if _, err := os.Stat(framePath); err == nil {
				// Frame exists, try to load it
				f, err := os.Open(framePath)
				if err != nil {
					consecutiveEmpty++
				} else {
					img, _, err := image.Decode(f)
					f.Close()
					if err != nil {
						consecutiveEmpty++
					} else {
						imgChan <- img
						frameNum++
						consecutiveEmpty = 0
						continue
					}
				}
			} else {
				consecutiveEmpty++
			}

			// Check if ffmpeg is still running
			if cmd.Process != nil {
				if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
					// Process is not running
					if consecutiveEmpty > maxConsecutiveEmpty {
						break
					}
				}
			}

			// Small delay before checking again
			time.Sleep(10 * time.Millisecond)

			if consecutiveEmpty > maxConsecutiveEmpty*3 {
				break
			}
		}

		// Wait for ffmpeg to finish
		cmd.Wait()
	}()

	return imgChan, errChan
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
	// Terminal chars are ~2x taller than wide, so we need to compensate
	charAspectRatio := 2.0 // height/width of a terminal character
	imageAspectRatio := float64(imgWidth) / float64(imgHeight)

	// First calculate height based on full terminal width
	newWidth := width
	// Calculate height accounting for character aspect ratio
	// If image is 16:9, and chars are 2:1, the effective aspect is (16/9)/2 = 0.89
	newHeight := int(float64(newWidth) / imageAspectRatio * charAspectRatio)

	// If height exceeds terminal, scale down to fit height
	if newHeight > height {
		newHeight = height
		newWidth = int(float64(newHeight) * imageAspectRatio / charAspectRatio)
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
	// Parse command line arguments
	var inputPath string
	flag.StringVar(&inputPath, "i", "./frames", "Input path (image file or directory)")
	flag.Parse()

	// If positional argument provided, use it as input path
	if len(flag.Args()) > 0 {
		inputPath = flag.Args()[0]
	}

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
	fmt.Printf("Terminal: %dx%d | Input: %s\n", width, height, inputPath)

	// Check if input is a file or directory
	fileInfo, err := os.Stat(inputPath)
	if err != nil {
		fmt.Printf("Error: cannot access '%s': %v\n", inputPath, err)
		os.Exit(1)
	}

	ext := strings.ToLower(filepath.Ext(inputPath))

	// Handle video files with streaming
	if !fileInfo.IsDir() && (ext == ".mp4" || ext == ".webm" || ext == ".mov" || ext == ".avi") {
		fmt.Printf("Streaming video: %s\n", inputPath)

		// Start streaming frames from video
		imgChan, errChan := streamVideoFrames(inputPath)

		frameRate := 30 // Match ffmpeg extraction rate
		frameDelay := time.Duration(1000.0/float64(frameRate)) * time.Millisecond

		frameCount := 0
		startTime := time.Now()

		// Wait for at least a few frames before starting
		time.Sleep(100 * time.Millisecond)

		for {
			select {
			case img, ok := <-imgChan:
				if !ok {
					// Channel closed, video finished
					fmt.Printf("\nPlayback complete. %d frames displayed in %v\n", frameCount, time.Since(startTime).Round(time.Second))
					return
				}
				clearScreen()
				printAscii(img, width, height)
				frameCount++

				// Show progress every 30 frames
				if frameCount%30 == 0 {
					fmt.Printf("\rFrame: %d | Time: %v", frameCount, time.Since(startTime).Round(time.Second))
				}

				time.Sleep(frameDelay)

			case err := <-errChan:
				if err != nil {
					fmt.Printf("\nError: %v\n", err)
					return
				}
			}
		}
	}

	// Handle static content (images and directories)
	var images []image.Image

	if fileInfo.IsDir() {
		// Process directory of frames
		images = processFramesFromFolder(inputPath)
		fmt.Println() // New line after progress bar
	} else {
		// Process single image file
		switch ext {
		case ".jpg", ".jpeg":
			file, err := os.Open(inputPath)
			if err != nil {
				panic(err)
			}
			defer file.Close()
			img, err := jpeg.Decode(file)
			if err != nil {
				panic(err)
			}
			images = append(images, img)
			fmt.Println("Loaded JPEG")
		case ".png":
			file, err := os.Open(inputPath)
			if err != nil {
				panic(err)
			}
			defer file.Close()
			img, err := png.Decode(file)
			if err != nil {
				panic(err)
			}
			images = append(images, img)
			fmt.Println("Loaded PNG")
		default:
			fmt.Printf("Error: unsupported file format '%s'\n", ext)
			fmt.Println("Supported formats: .jpg, .jpeg, .png, .mp4, .webm, .mov, .avi")
			os.Exit(1)
		}
	}

	if len(images) == 0 {
		fmt.Println("No images to display")
		return
	}

	frameRate := 50
	frameDelay := time.Duration(1000.0/float64(frameRate)) * time.Millisecond

	for _, img := range images {
		clearScreen()
		printAscii(img, width, height)
		time.Sleep(frameDelay)
	}
}
