package main

import (
	"fmt"
	"image"
	"image/draw"
	_ "image/jpeg"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/kkdai/youtube/v2"
	"golang.org/x/term"
)

const (
	grayR    = 0.299
	grayG    = 0.587
	grayB    = 0.114
	maxUint8 = 255
)

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

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: ./youtube <video_url>")
		fmt.Println("Example: ./youtube https://www.youtube.com/watch?v=dQw4w9WgXcQ")
		os.Exit(1)
	}

	videoURL := os.Args[1]

	if !term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Println("Not a terminal")
		return
	}

	width, height, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		panic(err)
	}
	height = height - 1
	fmt.Printf("Terminal: %dx%d | Loading YouTube: %s\n", width, height, videoURL)

	// Get YouTube stream
	_, _, stream, err := getYouTubeStream(videoURL)
	if err != nil {
		fmt.Printf("Error getting stream: %v\n", err)
		os.Exit(1)
	}
	defer stream.Close()

	// Start streaming frames directly from the video stream
	imgChan, errChan := streamYouTubeFramesFromStream(stream)

	frameRate := 30
	frameDelay := time.Duration(1000.0/float64(frameRate)) * time.Millisecond

	frameCount := 0
	startTime := time.Now()

	// Short wait for first frames to be extracted
	time.Sleep(500 * time.Millisecond)

	for {
		select {
		case img, ok := <-imgChan:
			if !ok {
				fmt.Printf("\nPlayback complete. %d frames displayed in %v\n", frameCount, time.Since(startTime).Round(time.Second))
				return
			}
			clearScreen()
			printAscii(img, width, height)
			frameCount++

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

func getYouTubeStream(url string) (*youtube.Video, *youtube.Format, io.ReadCloser, error) {
	client := youtube.Client{}

	video, err := client.GetVideo(url)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get video info: %w", err)
	}

	fmt.Printf("Title: %s\n", video.Title)
	fmt.Printf("Author: %s\n", video.Author)
	fmt.Printf("Duration: %v\n", video.Duration)

	formats := video.Formats.WithAudioChannels()
	if len(formats) == 0 {
		return nil, nil, nil, fmt.Errorf("no video formats found")
	}

	var bestFormat *youtube.Format
	for _, format := range formats {
		if format.QualityLabel == "720p" || format.QualityLabel == "480p" || format.QualityLabel == "360p" {
			bestFormat = &format
			break
		}
	}
	if bestFormat == nil {
		bestFormat = &formats[0]
	}

	fmt.Printf("Selected quality: %s\n", bestFormat.QualityLabel)
	fmt.Println("Starting playback...")

	stream, _, err := client.GetStream(video, bestFormat)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get stream: %w", err)
	}

	return video, bestFormat, stream, nil
}

func copyFile(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destFile.Close()

	_, err = io.Copy(destFile, sourceFile)
	return err
}

func streamYouTubeFramesFromStream(stream io.ReadCloser) (<-chan image.Image, chan error) {
	imgChan := make(chan image.Image, 30)
	errChan := make(chan error, 1)

	go func() {
		defer close(imgChan)
		defer close(errChan)

		// Create frames directory in current working directory
		framesDir := "./youtube_frames"
		err := os.MkdirAll(framesDir, 0755)
		if err != nil {
			errChan <- fmt.Errorf("failed to create frames directory: %w", err)
			return
		}
		defer os.RemoveAll(framesDir)

		framePattern := filepath.Join(framesDir, "frame_%05d.jpg")

		fmt.Println("Extracting frames...")

		// Start ffmpeg with stdin input for streaming
		cmd := exec.Command("ffmpeg",
			"-hide_banner",
			"-loglevel", "error",
			"-i", "-", // Read from stdin
			"-vf", "fps=30,scale=480:-1:flags=lanczos",
			"-pix_fmt", "yuvj420p",
			"-q:v", "2",
			framePattern,
		)

		// Get stdin pipe to write video data
		stdin, err := cmd.StdinPipe()
		if err != nil {
			errChan <- fmt.Errorf("failed to get stdin pipe: %w", err)
			return
		}

		// Start ffmpeg
		if err := cmd.Start(); err != nil {
			errChan <- fmt.Errorf("failed to start ffmpeg: %w", err)
			return
		}

		// Copy video stream to ffmpeg stdin in a separate goroutine
		go func() {
			defer stdin.Close()
			io.Copy(stdin, stream)
		}()

		// Track processed frames
		processedFrames := make(map[string]bool)
		ffmpegExited := false
		emptyIterations := 0
		maxEmptyIterations := 600 // About 6 seconds of waiting
		totalFramesSent := 0

		for {
			// Read all frame files from directory
			entries, err := os.ReadDir(framesDir)
			if err != nil {
				time.Sleep(10 * time.Millisecond)
				continue
			}

			// Find unprocessed frame files and sort them
			var frameFiles []string
			for _, entry := range entries {
				if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jpg") {
					framePath := filepath.Join(framesDir, entry.Name())
					if !processedFrames[framePath] {
						frameFiles = append(frameFiles, framePath)
					}
				}
			}

			// Debug: print first time we find frames
			if len(frameFiles) > 0 && totalFramesSent == 0 {
				fmt.Printf("\nFound %d frames in directory\n", len(frameFiles))
			}

			// Sort frame files to process in order
			// Note: frame_%05d.jpg sorts correctly alphabetically
			for i := 0; i < len(frameFiles)-1; i++ {
				for j := i + 1; j < len(frameFiles); j++ {
					if frameFiles[i] > frameFiles[j] {
						frameFiles[i], frameFiles[j] = frameFiles[j], frameFiles[i]
					}
				}
			}

			// Process available frames
			framesProcessed := 0
			for _, framePath := range frameFiles {
				f, err := os.Open(framePath)
				if err != nil {
					fmt.Printf("\nError opening frame %s: %v\n", framePath, err)
					continue
				}

				img, format, err := image.Decode(f)
				f.Close()

				if err != nil {
					fmt.Printf("\nError decoding frame %s (format: %s): %v\n", framePath, format, err)
					processedFrames[framePath] = true // Mark as processed even if decode fails
					os.Remove(framePath)
					continue
				}

				imgChan <- img
				processedFrames[framePath] = true
				os.Remove(framePath)
				framesProcessed++
				totalFramesSent++
				emptyIterations = 0 // Reset empty counter when we process frames
			}

			// Debug: print progress every 30 frames
			if totalFramesSent > 0 && totalFramesSent%30 == 0 && framesProcessed > 0 {
				fmt.Printf("\rSent %d frames...", totalFramesSent)
			}

			// Check if ffmpeg has finished
			if cmd.Process != nil && !ffmpegExited {
				if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
					ffmpegExited = true
					fmt.Printf("\nFFmpeg finished, processed %d frames total\n", totalFramesSent)
				}
			}

			// If no new frames were found this iteration
			if framesProcessed == 0 {
				emptyIterations++
			}

			// Exit conditions
			if ffmpegExited && emptyIterations > 100 {
				// FFmpeg done and no new frames for ~1 second
				fmt.Printf("\nFinished processing %d frames\n", totalFramesSent)
				break
			}

			if !ffmpegExited && emptyIterations > maxEmptyIterations {
				// Timeout waiting for frames
				fmt.Printf("\nWarning: Timeout waiting for frames (found %d frames)\n", totalFramesSent)
				break
			}

			time.Sleep(10 * time.Millisecond)
		}

		cmd.Wait()
	}()

	return imgChan, errChan
}

func clearScreen() {
	fmt.Print("\033[H\033[2J")
}

func processImage(img image.Image) image.Image {
	bounds := img.Bounds()
	grayImg := image.NewGray(bounds)
	draw.Draw(grayImg, grayImg.Bounds(), img, img.Bounds().Min, draw.Src)
	pa := newPixelAccessor(grayImg)

	var histogram [256]int
	var minGray, maxGray uint8 = maxUint8, 0

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			g := pa.at(x, y)
			histogram[g]++
			if g < minGray {
				minGray = g
			}
			if g > maxGray {
				maxGray = g
			}
		}
	}

	if minGray == maxGray {
		return grayImg
	}

	totalPixels := (bounds.Max.X - bounds.Min.X) * (bounds.Max.Y - bounds.Min.Y)
	clipPercent := int(float64(totalPixels) * 0.02)

	cumsum := 0
	newMin := uint8(0)
	for i := 0; i < 256; i++ {
		cumsum += histogram[i]
		if cumsum >= clipPercent {
			newMin = uint8(i)
			break
		}
	}

	cumsum = 0
	newMax := uint8(255)
	for i := 255; i >= 0; i-- {
		cumsum += histogram[i]
		if cumsum >= clipPercent {
			newMax = uint8(i)
			break
		}
	}

	if newMax <= newMin {
		newMin = minGray
		newMax = maxGray
	}

	contrastImg := image.NewGray(bounds)
	contrastPA := newPixelAccessor(contrastImg)

	scaleFactor := float64(maxUint8) / float64(newMax-newMin)
	brightnessBoost := 1.1

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			originalGray := pa.at(x, y)
			var stretched float64
			if originalGray < newMin {
				stretched = 0
			} else if originalGray > newMax {
				stretched = 255
			} else {
				stretched = float64(originalGray-newMin) * scaleFactor
			}
			stretched = stretched * brightnessBoost
			if stretched > 255 {
				stretched = 255
			}
			contrastPA.set(x, y, uint8(stretched))
		}
	}

	return contrastImg
}

func resizeImage(img image.Image, targetWidth, targetHeight int) image.Image {
	srcBounds := img.Bounds()
	srcWidth := srcBounds.Dx()
	srcHeight := srcBounds.Dy()

	dst := image.NewRGBA(image.Rect(0, 0, targetWidth, targetHeight))
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

func printAscii(img image.Image, width, height int) {
	darkToLight := "@#%&*+=-;:,. "
	numCharsInRamp := len(darkToLight)

	bounds := img.Bounds()
	imgWidth := bounds.Dx()
	imgHeight := bounds.Dy()

	charAspectCorrection := 2.0
	imageAspectRatio := float64(imgWidth) / float64(imgHeight)

	newWidth := width
	newHeight := int(float64(newWidth) / imageAspectRatio / charAspectCorrection)

	if newHeight > height {
		newHeight = height
		newWidth = int(float64(newHeight) * imageAspectRatio * charAspectCorrection)
	}

	if newWidth > width {
		newWidth = width
	}
	if newHeight > height {
		newHeight = height
	}
	if newWidth < 1 {
		newWidth = 1
	}
	if newHeight < 1 {
		newHeight = 1
	}

	resizedImg := resizeImage(img, newWidth, newHeight)
	processedImg := processImage(resizedImg)

	bounds = processedImg.Bounds()

	var sb strings.Builder
	sb.Grow(newWidth*newHeight + newHeight)

	var grayImg *image.Gray
	var pix []uint8
	var stride int

	if g, ok := processedImg.(*image.Gray); ok {
		grayImg = g
		pix = g.Pix
		stride = g.Stride
	}

	charScale := float64(numCharsInRamp) / 256.0
	gamma := 0.85

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			var gray uint8

			if grayImg != nil {
				idx := (y-bounds.Min.Y)*stride + (x - bounds.Min.X)
				gray = pix[idx]
			} else {
				r, g, b, _ := processedImg.At(x, y).RGBA()
				gray = uint8(grayR*float64(uint8(r>>8)) +
					grayG*float64(uint8(g>>8)) +
					grayB*float64(uint8(b>>8)))
			}

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
