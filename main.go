package main

import (
	"fmt"
	"golang.org/x/term"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func resizeImage(img image.Image, targetWidth, targetHeight int) image.Image {
	srcBounds := img.Bounds()
	srcWidth := srcBounds.Dx()
	srcHeight := srcBounds.Dy()

	dst := image.NewRGBA(image.Rect(0, 0, targetWidth, targetHeight))

	for y := range targetHeight {
		for x := range targetWidth {
			srcX := x * srcWidth / targetWidth
			srcY := y * srcHeight / targetHeight
			c := img.At(srcX+srcBounds.Min.X, srcY+srcBounds.Min.Y)
			dst.Set(x, y, c)
		}
	}

	return dst
}

func clearScreen() {
	fmt.Print("\033[H\033[2J")
}

// func processFrames: gets the ref to list of images, gets vieo and appends the images to the list
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

	var images []image.Image
	total := len(files)
	for i, file := range files {
		if file.IsDir() {
			continue
		}
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
		images = append(images, img)
		renderProgressBar(i+1, total, 40)
	}

	return images
}

func processImage(img image.Image) image.Image {
	// Get the bounds and dimensions of the original image.
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	// 1. Convert the image to grayscale.
	// Create a new grayscale image with the same dimensions as the input image.
	grayImg := image.NewGray(bounds)
	// Draw the input image onto the new grayscale image, performing color conversion.
	draw.Draw(grayImg, grayImg.Bounds(), img, img.Bounds().Min, draw.Src)

	// 2. Enhance contrast of the grayscale image (Histogram Stretching).
	var minGray, maxGray uint8
	minGray = 255 // Initialize minGray to the highest possible grayscale value.
	maxGray = 0   // Initialize maxGray to the lowest possible grayscale value.

	// Iterate over each pixel of the grayscale image to find the actual min and max gray values.
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			g := grayImg.GrayAt(x, y).Y
			if g < minGray {
				minGray = g
			}
			if g > maxGray {
				maxGray = g
			}
		}
	}

	// Create a new grayscale image for the contrast-adjusted output.
	contrastEnhancedGrayImg := image.NewGray(bounds)

	// Handle the edge case where the image is a single flat color.
	if minGray == maxGray {
		uniformColor := color.Gray{Y: minGray}
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				contrastEnhancedGrayImg.SetGray(x, y, uniformColor)
			}
		}
		// If the image is flat, Sobel edge detection will result in a black image (no edges),
		// so we let it proceed to the Sobel step.
	} else {
		// Calculate the scaling factor for contrast stretching.
		scaleFactor := 255.0 / float64(maxGray-minGray)
		// Apply the contrast stretching transformation to each pixel.
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				originalGray := grayImg.GrayAt(x, y).Y
				stretchedGray := float64(originalGray-minGray) * scaleFactor

				// Clamp values to the 0-255 range.
				if stretchedGray < 0 {
					stretchedGray = 0
				}
				if stretchedGray > 255 {
					stretchedGray = 255
				}
				contrastEnhancedGrayImg.SetGray(x, y, color.Gray{Y: uint8(stretchedGray)})
			}
		}
	}
	// At this point, contrastEnhancedGrayImg contains the contrast-enhanced grayscale image.

	// 3. Sobel Edge Detection.
	// The input for Sobel is the contrastEnhancedGrayImg.
	// The output will be edgeImg.
	edgeImg := image.NewGray(bounds) // Initialize with all black pixels. Borders will remain black.

	// Sobel kernels for edge detection.
	sobelX := [][]int{
		{-1, 0, 1},
		{-2, 0, 2},
		{-1, 0, 1},
	}
	sobelY := [][]int{
		{-1, -2, -1},
		{0, 0, 0},
		{1, 2, 1},
	}

	// Store raw gradient magnitudes for dynamic normalization.
	// This 2D slice will store magnitudes corresponding to each pixel (excluding borders).
	// Indexed by [relative_y][relative_x] from the image's top-left (bounds.Min).
	magnitudes := make([][]float64, height)
	for i := 0; i < height; i++ {
		magnitudes[i] = make([]float64, width)
	}

	var maxFoundMagnitude float64 = 0.0 // To normalize the magnitudes later.

	// Apply Sobel operator. We iterate over pixels where the 3x3 kernel fits,
	// skipping a 1-pixel border around the image.
	// These border pixels in edgeImg will remain black (0).
	for y_abs := bounds.Min.Y + 1; y_abs < bounds.Max.Y-1; y_abs++ {
		for x_abs := bounds.Min.X + 1; x_abs < bounds.Max.X-1; x_abs++ {
			var gx, gy float64 // Gradient in X and Y directions.

			// Apply 3x3 kernels.
			for ky := -1; ky <= 1; ky++ { // Kernel y-offset.
				for kx := -1; kx <= 1; kx++ { // Kernel x-offset.
					// Get pixel value from the contrast-enhanced grayscale image.
					// (x_abs + kx, y_abs + ky) are absolute coordinates in the image.
					pixelVal := float64(contrastEnhancedGrayImg.GrayAt(x_abs+kx, y_abs+ky).Y)
					gx += pixelVal * float64(sobelX[ky+1][kx+1]) // ky+1, kx+1 to map -1..1 to 0..2 for kernel array index.
					gy += pixelVal * float64(sobelY[ky+1][kx+1])
				}
			}

			// Calculate gradient magnitude.
			magnitude := math.Sqrt(gx*gx + gy*gy)

			// Store magnitude. y_rel and x_rel are 0-indexed relative to bounds.Min.
			y_rel := y_abs - bounds.Min.Y
			x_rel := x_abs - bounds.Min.X
			magnitudes[y_rel][x_rel] = magnitude

			if magnitude > maxFoundMagnitude {
				maxFoundMagnitude = magnitude
			}
		}
	}

	// Normalize magnitudes to 0-255 range and set them into the edgeImg.
	if maxFoundMagnitude > 0 { // Avoid division by zero if image was flat and had no edges.
		for y_abs := bounds.Min.Y + 1; y_abs < bounds.Max.Y-1; y_abs++ {
			for x_abs := bounds.Min.X + 1; x_abs < bounds.Max.X-1; x_abs++ {
				y_rel := y_abs - bounds.Min.Y
				x_rel := x_abs - bounds.Min.X
				// Normalize the stored magnitude.
				normalizedVal := (magnitudes[y_rel][x_rel] / maxFoundMagnitude) * 255.0

				// Clamping (though normalization should keep it in range if maxFoundMagnitude is correct).
				// uint8 conversion will also clamp, but explicit is safer.
				if normalizedVal > 255 {
					normalizedVal = 255
				}
				// Magnitudes are non-negative, so no < 0 check needed for normalizedVal here.

				edgeImg.SetGray(x_abs, y_abs, color.Gray{Y: uint8(normalizedVal)})
			}
		}
	}
	// Pixels on the 1-pixel border of edgeImg remain black (0) as they were not processed by Sobel.

	// Return the edge-detected image.
	return edgeImg
}

func printAscii(img image.Image, width, height int) {
	// darkToLight := "#%*+=-'. "
	darkToLight := " .'-=+*%#"
	numCharsInRamp := len(darkToLight)

	bounds := img.Bounds()

	imgWidth := bounds.Max.X - bounds.Min.X
	imgHeight := bounds.Max.Y - bounds.Min.Y

	// get new width and height for image to fit in terminal

	aspectRatio := float64(imgWidth) / float64(imgHeight)
	newWidth := width
	newHeight := int(float64(newWidth) / aspectRatio)

	if newHeight > height {
		newHeight = height
		newWidth = int(float64(newHeight) * aspectRatio)
	}

	resizedImg := resizeImage(img, newWidth, newHeight)
	imgWidth = resizedImg.Bounds().Max.X - resizedImg.Bounds().Min.X
	imgHeight = resizedImg.Bounds().Max.Y - resizedImg.Bounds().Min.Y
	bounds = resizedImg.Bounds()

	// do some image processing to make it better for ascii art
	processedImg := processImage(resizedImg)
	// processedImg := resizedImg

	// Characters from darkest to lightest
	// darkToLight := "#$@B%8&*oakbqwZOLCJzcvxrjft/|()1{}[]?-+~<>!:,^`'. "

	if numCharsInRamp == 0 {
		fmt.Println("Error: darkToLight string is empty, cannot generate ASCII art.")
		return
	}

	lines := ""

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		line := ""
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, _ := processedImg.At(x, y).RGBA() // Returns values in range [0, 0xFFFF]

			// Convert to 8-bit values (0-255)
			r8 := uint8(r >> 8)
			g8 := uint8(g >> 8)
			b8 := uint8(b >> 8)

			gray := uint8(0.299*float64(r8) + 0.587*float64(g8) + 0.114*float64(b8))

			characterIndex := int(float64(gray) * float64(numCharsInRamp) / 256.0)

			if characterIndex >= numCharsInRamp {
				characterIndex = numCharsInRamp - 1
			}
			if characterIndex < 0 { // Should not happen with uint8 gray
				characterIndex = 0
			}

			character := darkToLight[characterIndex]
			// fmt.Printf("%d %d %d ", r8, g8, b8)
			line += string(character)
		}
		lines += line + "\n"
	}
	fmt.Println(lines)
}

func main() {
	// check if terminal and get size
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
		panic(err) // Or handle error more gracefully, e.g., log.Fatal(err)
	}
	defer file.Close()

	// test if the file is a jpeg or png

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
	}

	frameRate := 50
	frameDelay := time.Duration(1000.0/frameRate) * time.Millisecond

	for _, img := range images {
		clearScreen()
		printAscii(img, width, height)
		time.Sleep(frameDelay)
	}

}
