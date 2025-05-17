# Go ASCII Art Generator

This Go program converts an input image (JPEG or PNG) into ASCII art and displays it in the terminal. It automatically detects the terminal size and resizes the image to fit the width while maintaining its aspect ratio.

## Features

* Detects if the program is run within a terminal.
* Retrieves the current terminal width and height.
* Supports JPEG and PNG image formats.
* Automatically decodes the input image (`test1.jpg` by default).
* Resizes the image to fit the terminal's width, preserving the aspect ratio.
    * The resizing algorithm used is a simple nearest-neighbor approach.
* Converts the resized image to grayscale.
    * Grayscale conversion uses the formula: $0.299 \times R + 0.587 \times G + 0.114 \times B$.
* Maps grayscale pixel values to a predefined ramp of ASCII characters.
* Prints the resulting ASCII art to the console.

## Prerequisites

* Go (Golang) installed on your system.
* An image file (e.g., `test1.jpg`) in the same directory as the compiled executable, or provide the correct path in the code.

## Dependencies

The program uses the following Go packages:

* `fmt`: For formatted I/O.
* `golang.org/x/term`: To get terminal dimensions and check if running in a terminal.
* `image`: For basic image manipulation (part of the standard library).
* `image/jpeg`: For decoding JPEG images (part of the standard library).
* `image/png`: For decoding PNG images (part of the standard library).
* `os`: For file system operations like opening files.
* `path/filepath`: For manipulating file paths (e.g., getting extensions).
* `strings`: For string manipulation.

To install the `golang.org/x/term` package if you haven't already:
```bash
go get golang.org/x/term
```


# TODO:
- Add support for video
- Add support for online sources: image and video
