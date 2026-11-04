package order

import (
	"bytes"
	"encoding/base64"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"strings"
	"unicode/utf8"
)

type actionRequest struct {
	Action      string `json:"action"`
	Description string `json:"description,omitempty"`
	Photo       string `json:"photo,omitempty"`
	Explanation string `json:"explanation,omitempty"`
}

func (b *actionRequest) validate() error {
	b.Description = strings.TrimSpace(b.Description)
	b.Explanation = strings.TrimSpace(b.Explanation)
	if b.Action == "dispute" && (b.Description == "" || utf8.RuneCountInString(b.Description) > 2000 || !validText(b.Description, true)) {
		return problem(400, "invalid_description", "Explain why you are disputing this delivery (1–2000 characters).")
	}
	if (b.Action == "resolve_completed" || b.Action == "resolve_not_completed") && (b.Explanation == "" || utf8.RuneCountInString(b.Explanation) > 2000 || !validText(b.Explanation, true)) {
		return problem(400, "invalid_explanation", "Explain your dispute decision (1–2000 characters).")
	}
	if (b.Description != "" && b.Action != "dispute") || (b.Explanation != "" && b.Action != "resolve_completed" && b.Action != "resolve_not_completed") || (b.Photo != "" && b.Action != "deliver" && b.Action != "dispute") {
		return problem(400, "invalid_action_fields", "Evidence can only be submitted with its delivery, dispute or resolution action.")
	}
	if b.Photo == "" {
		return nil
	}
	invalid := func() error {
		return problem(400, "invalid_photo", "Choose a valid JPEG or PNG photo up to 512 KiB and 4096 pixels per side.")
	}
	header, encoded, ok := strings.Cut(b.Photo, ",")
	if !ok || (header != "data:image/jpeg;base64" && header != "data:image/png;base64") || len(encoded) > base64.StdEncoding.EncodedLen(512*1024) {
		return invalid()
	}
	data, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(data) == 0 || len(data) > 512*1024 {
		return invalid()
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || header != "data:image/"+format+";base64" || config.Width < 1 || config.Height < 1 || config.Width > 4096 || config.Height > 4096 {
		return invalid()
	}
	if _, _, err = image.Decode(bytes.NewReader(data)); err != nil {
		return invalid()
	}
	return nil
}
