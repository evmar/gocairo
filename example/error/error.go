package main

import (
	"github.com/martine/gocairo/cairo"
)

func main() {
	// Normally you should use one of the cairo.FormatXXX constants, but
	// this program is demonstrating a panic.
	cairo.ImageSurfaceCreate(cairo.Format(1000), 640, 480)
}
