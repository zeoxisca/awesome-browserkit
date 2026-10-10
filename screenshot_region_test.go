package browserkit

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func TestCropScreenshotUsesViewportCoordinates(t *testing.T) {
	whole := image.NewRGBA(image.Rect(0, 0, 200, 100))
	whole.Set(100, 50, color.RGBA{R: 255, A: 255})
	var source bytes.Buffer
	if err := png.Encode(&source, whole); err != nil {
		t.Fatal(err)
	}
	data, err := cropScreenshot(source.Bytes(), ScreenshotRegion{X: 50, Y: 25, Width: 50, Height: 25, ViewportWidth: 100, ViewportHeight: 50})
	if err != nil {
		t.Fatal(err)
	}
	cropped, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, a := cropped.At(0, 0).RGBA()
	if cropped.Bounds().Dx() != 100 || cropped.Bounds().Dy() != 50 || r != 0xffff || g != 0 || b != 0 || a != 0xffff {
		t.Fatalf("unexpected cropped image bounds=%v first=%v", cropped.Bounds(), cropped.At(0, 0))
	}
}
