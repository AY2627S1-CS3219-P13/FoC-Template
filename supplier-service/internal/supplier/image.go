package supplier

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/draw"
	"image/jpeg"
	_ "image/png"
	"strings"
)

// Supplier photos use Order's delivery-photo format: a base64 JPEG or PNG data
// URL of at most 512 KiB and 4096 pixels per side, stored as text.
const (
	maxImageBytes = 512 * 1024
	maxImageSide  = 4096
)

// normaliseImage validates a photo and re-encodes it as JPEG. Re-encoding drops
// metadata such as a phone's GPS location and flattens transparency onto white.
// An empty value means no photo.
func normaliseImage(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	invalid := problem(400, "invalid_image", "Choose a valid JPEG or PNG image up to 512 KiB and 4096 pixels per side.")
	header, encoded, ok := strings.Cut(value, ",")
	if !ok || (header != "data:image/jpeg;base64" && header != "data:image/png;base64") || len(encoded) > base64.StdEncoding.EncodedLen(maxImageBytes) {
		return "", invalid
	}
	data, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(data) == 0 || len(data) > maxImageBytes {
		return "", invalid
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || header != "data:image/"+format+";base64" || config.Width < 1 || config.Height < 1 || config.Width > maxImageSide || config.Height > maxImageSide {
		return "", invalid
	}
	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", invalid
	}
	if format == "png" {
		flat := image.NewRGBA(decoded.Bounds())
		draw.Draw(flat, flat.Bounds(), image.White, image.Point{}, draw.Src)
		draw.Draw(flat, flat.Bounds(), decoded, decoded.Bounds().Min, draw.Over)
		decoded = flat
	}
	for _, quality := range []int{90, 80, 65, 50} {
		var out bytes.Buffer
		if err = jpeg.Encode(&out, decoded, &jpeg.Options{Quality: quality}); err != nil {
			return "", err
		}
		if out.Len() <= maxImageBytes {
			return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(out.Bytes()), nil
		}
	}
	return "", invalid
}
