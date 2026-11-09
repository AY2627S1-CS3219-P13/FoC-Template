package supplier

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

func dataURL(kind string, data []byte) string {
	return "data:image/" + kind + ";base64," + base64.StdEncoding.EncodeToString(data)
}

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func decodeStored(t *testing.T, value string) image.Image {
	t.Helper()
	header, encoded, ok := strings.Cut(value, ",")
	if !ok || header != "data:image/jpeg;base64" {
		t.Fatalf("stored photo is not a JPEG data URL: %.40s", value)
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil || format != "jpeg" {
		t.Fatalf("stored photo does not decode as JPEG: %v", err)
	}
	return img
}

func TestNormaliseImageReencodesAsJPEG(t *testing.T) {
	if value, err := normaliseImage(""); value != "" || err != nil {
		t.Fatal("an empty photo must mean no photo")
	}
	// A transparent PNG is flattened onto white rather than black.
	img := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	img.Set(0, 0, color.NRGBA{R: 200, A: 255})
	stored, err := normaliseImage(dataURL("png", encodePNG(t, img)))
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := decodeStored(t, stored).At(7, 7).RGBA()
	if r>>8 < 240 || g>>8 < 240 || b>>8 < 240 {
		t.Fatalf("transparent pixel became %d,%d,%d instead of white", r>>8, g>>8, b>>8)
	}
}

func TestNormaliseImageDropsMetadata(t *testing.T) {
	var plain bytes.Buffer
	if err := jpeg.Encode(&plain, image.NewRGBA(image.Rect(0, 0, 4, 4)), nil); err != nil {
		t.Fatal(err)
	}
	// Insert an EXIF segment carrying a GPS marker straight after the JPEG start.
	payload := append([]byte("Exif\x00\x00"), []byte("GPSLatitude 1.2966")...)
	segment := append([]byte{0xFF, 0xE1, byte((len(payload) + 2) >> 8), byte(len(payload) + 2)}, payload...)
	withExif := append(append(append([]byte{}, plain.Bytes()[:2]...), segment...), plain.Bytes()[2:]...)
	stored, err := normaliseImage(dataURL("jpeg", withExif))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, "data:image/jpeg;base64,"))
	if bytes.Contains(data, []byte("Exif")) || bytes.Contains(data, []byte("GPS")) {
		t.Fatal("photo metadata survived re-encoding")
	}
}

func TestNormaliseImageRejectsInvalidPhotos(t *testing.T) {
	small := encodePNG(t, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	var gif bytes.Buffer
	gif.WriteString("GIF89a")
	for name, value := range map[string]string{
		"no comma":            "data:image/png;base64",
		"unsupported type":    dataURL("gif", gif.Bytes()),
		"type mismatch":       dataURL("jpeg", small),
		"not base64":          "data:image/png;base64,!!!!",
		"padding inside":      "data:image/png;base64," + base64.StdEncoding.EncodeToString(small)[:8] + "====" + base64.StdEncoding.EncodeToString(small)[8:],
		"empty data":          "data:image/png;base64,",
		"not an image":        dataURL("png", []byte("hello")),
		"truncated image":     dataURL("png", small[:len(small)-12]),
		"too wide":            dataURL("png", encodePNG(t, image.NewGray(image.Rect(0, 0, maxImageSide+1, 1)))),
		"too tall":            dataURL("png", encodePNG(t, image.NewGray(image.Rect(0, 0, 1, maxImageSide+1)))),
		"over 512 KiB":        dataURL("png", append(small, make([]byte, maxImageBytes)...)),
		"plain URL":           "https://example.com/photo.jpg",
		"uppercase data type": strings.Replace(dataURL("png", small), "image/png", "IMAGE/PNG", 1),
	} {
		_, err := normaliseImage(value)
		var e *apiError
		if !errors.As(err, &e) || e.status != 400 || e.code != "invalid_image" {
			t.Errorf("%s: got %v, want invalid_image", name, err)
		}
	}
}

func TestNormaliseImageAcceptsLimits(t *testing.T) {
	// The largest allowed side, with highly compressible content.
	img := image.NewGray(image.Rect(0, 0, maxImageSide, 1))
	stored, err := normaliseImage(dataURL("png", encodePNG(t, img)))
	if err != nil {
		t.Fatal(err)
	}
	if bounds := decodeStored(t, stored).Bounds(); bounds.Dx() != maxImageSide || bounds.Dy() != 1 {
		t.Fatalf("re-encoding changed the size to %v", bounds)
	}
	if len(stored) > 700000 {
		t.Fatal("stored photo exceeds the database limit")
	}
	// Go's decoder ignores line breaks in base64; the stored copy has none.
	encoded := base64.StdEncoding.EncodeToString(encodePNG(t, image.NewRGBA(image.Rect(0, 0, 2, 2))))
	stored, err = normaliseImage("data:image/png;base64," + encoded[:8] + "\r\n" + encoded[8:])
	if err != nil || strings.ContainsAny(stored, "\r\n") {
		t.Fatalf("a line-wrapped photo was not stored canonically: %v", err)
	}
}
