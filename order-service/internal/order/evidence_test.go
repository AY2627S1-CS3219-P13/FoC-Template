package order

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"strings"
	"testing"
)

func evidencePhoto(t *testing.T) string {
	t.Helper()
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(data.Bytes())
}

func TestEvidenceValidation(t *testing.T) {
	for _, b := range []actionRequest{
		{Action: "dispute"}, {Action: "dispute", Description: " \n\t"},
		{Action: "dispute", Description: strings.Repeat("a", 2001)},
		{Action: "dispute", Description: "invalid\x00text"},
		{Action: "resolve_completed"}, {Action: "resolve_not_completed", Explanation: " \n"},
		{Action: "resolve_completed", Explanation: strings.Repeat("a", 2001)},
		{Action: "deliver", Photo: "https://example.com/photo.png"},
		{Action: "deliver", Photo: "data:image/svg+xml;base64,PHN2Zy8+"},
		{Action: "deliver", Photo: "data:image/png;base64,bm90LWFuLWltYWdl"},
		{Action: "deliver", Photo: "data:image/jpeg;base64," + strings.Split(evidencePhoto(t), ",")[1]},
		{Action: "deliver", Photo: "data:image/png;base64," + strings.Repeat("A", 700000)},
		{Action: "accept", Photo: evidencePhoto(t)},
		{Action: "confirm", Description: "extra"},
		{Action: "dispute", Description: "valid", Explanation: "extra"},
	} {
		if err := b.validate(); err == nil {
			t.Fatalf("invalid evidence accepted for %s", b.Action)
		}
	}
	for _, b := range []actionRequest{
		{Action: "deliver"}, {Action: "deliver", Photo: evidencePhoto(t)},
		{Action: "dispute", Description: " Missing items\nPlease review. ", Photo: evidencePhoto(t)},
		{Action: "resolve_completed", Explanation: "Evidence confirms delivery."},
		{Action: "resolve_not_completed", Explanation: "The item was not delivered."},
	} {
		if err := b.validate(); err != nil {
			t.Fatalf("%s: %v", b.Action, err)
		}
	}
}

func TestEvidenceLifecycleAndImmutability(t *testing.T) {
	for _, outcome := range []string{"resolve_completed", "resolve_not_completed"} {
		t.Run(outcome, func(t *testing.T) {
			a, _ := testApp(t)
			a.auth = actorAuth{}
			o := createdForTest(t, a, outcome)
			path := "/api/v1/orders/" + o.ID + "/actions"
			post := func(token string, body map[string]string, status int) {
				t.Helper()
				got := request(t, a, "POST", path, token, "", body)
				if got.Code != status {
					t.Fatalf("%s: %d %s", body["action"], got.Code, got.Body.String())
				}
			}
			mustAction(t, a, o.ID, "courier", "accept", "accepted")
			mustAction(t, a, o.ID, "courier", "pick_up", "picked up")
			photo := evidencePhoto(t)
			post("requester", map[string]string{"action": "deliver", "photo": photo}, 403)
			post("courier", map[string]string{"action": "deliver", "photo": photo}, 200)
			version := readStored(t, a, o.ID).Version
			for _, description := range []string{"", " \n\t"} {
				post("requester", map[string]string{"action": "dispute", "description": description}, 400)
			}
			if readStored(t, a, o.ID).Version != version {
				t.Fatal("invalid dispute changed the order")
			}
			post("courier", map[string]string{"action": "dispute", "description": "Missing item", "photo": photo}, 403)
			post("requester", map[string]string{"action": "dispute", "description": " Missing item ", "photo": photo}, 200)
			original := readStored(t, a, o.ID)
			if original.DeliveryPhoto != photo || original.DisputePhoto != photo || original.DisputeDescription != "Missing item" {
				t.Fatal("evidence not persisted")
			}
			// A changed replay is not an edit: the original committed evidence wins.
			var replacement bytes.Buffer
			if err := png.Encode(&replacement, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
				t.Fatal(err)
			}
			post("requester", map[string]string{"action": "dispute", "description": "Replacement", "photo": "data:image/png;base64," + base64.StdEncoding.EncodeToString(replacement.Bytes())}, 200)
			post("requester", map[string]string{"action": "dispute", "description": "Attempt to remove photo"}, 200)
			post("courier", map[string]string{"action": "deliver"}, 200)
			stored := readStored(t, a, o.ID)
			if stored.DisputeDescription != original.DisputeDescription || stored.DisputePhoto != photo || stored.DeliveryPhoto != photo || stored.Version != original.Version {
				t.Fatal("replay edited submitted evidence")
			}
			// The mutation guard also rejects edits by internal refresh writers.
			edited := stored
			edited.DisputePhoto = ""
			tx, err := a.db.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if _, err = saveChange(context.Background(), tx, stored, edited); err == nil {
				t.Fatal("evidence edit allowed")
			}
			rollback(tx)
			for _, action := range []string{"resolve_completed", "resolve_not_completed"} {
				post("admin", map[string]string{"action": action}, 400)
			}
			post("requester", map[string]string{"action": outcome, "explanation": "Not authorized"}, 403)
			post("admin", map[string]string{"action": outcome, "explanation": " Reviewed all evidence. "}, 200)
			resolved := readStored(t, a, o.ID)
			post("admin", map[string]string{"action": outcome, "explanation": "Replacement"}, 200)
			post("requester", map[string]string{"action": "dispute", "description": "Replacement after resolution"}, 200)
			stored = readStored(t, a, o.ID)
			if stored.ResolutionExplanation != "Reviewed all evidence." || stored.DisputeDescription != original.DisputeDescription || stored.DisputePhoto != photo || stored.Version != resolved.Version {
				t.Fatal("resolution or evidence changed on replay")
			}
			got := request(t, a, "GET", "/api/v1/orders/"+o.ID, "requester", "", nil)
			var response struct {
				Order Order `json:"order"`
			}
			if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &response) != nil || response.Order.DisputePhoto != photo || response.Order.ResolutionExplanation != stored.ResolutionExplanation {
				t.Fatal("evidence missing from authorized detail")
			}
			if got := request(t, a, "GET", "/api/v1/orders/"+o.ID, "other", "", nil); got.Code != 404 {
				t.Fatal("evidence exposed to unrelated user")
			}
		})
	}
}
