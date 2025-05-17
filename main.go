package main

import (
	"fmt"
	"golang.org/x/term"
	"image"
	"image/jpeg"
	"image/png"
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

func printAscii(img image.Image, width, height int) {
	darkToLight := "@%#*+=-:. "
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

	// Characters from darkest to lightest
	// darkToLight := "#$@B%8&*oakbqwZOLCJzcvxrjft/|()1{}[]?-+~<>!:,^`'. "

	if numCharsInRamp == 0 {
		fmt.Println("Error: darkToLight string is empty, cannot generate ASCII art.")
		return
	}

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, _ := resizedImg.At(x, y).RGBA() // Returns values in range [0, 0xFFFF]

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
			fmt.Printf("%c", character)
		}
		fmt.Println()
	}
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

	frameRate := 20
	frameDelay := time.Duration(1000.0/frameRate) * time.Millisecond

	for _, img := range images {
		clearScreen()
		printAscii(img, width, height)
		time.Sleep(frameDelay)
	}

}
