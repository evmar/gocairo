package main

import (
	"log"

	"github.com/martine/gocairo/cairo"
)

func main() {
	log.Printf("cairo version %d/%s", cairo.Version(), cairo.VersionString())

	surf := cairo.ImageSurfaceCreate(cairo.FormatRgb24, 640, 480)
	cr := cairo.Create(surf.Surface)

	cr.SetSourceRgb(0, 0, 0)
	cr.Paint()

	cr.SetSourceRgb(1, 0, 0)
	cr.SelectFontFace("monospace", cairo.FontSlantNormal, cairo.FontWeightNormal)
	cr.SetFontSize(50)
	cr.MoveTo(640/10, 480/2)
	cr.ShowText("hello, world")

	surf.WriteToPng("foobar.png")
	log.Printf("wrote foobar.png")
}
