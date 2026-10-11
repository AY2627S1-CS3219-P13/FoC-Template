package order

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"
)

type PlaceRef struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type Place struct {
	Type         string `json:"type"`
	ID           string `json:"id"`
	Name         string `json:"name"`
	LocationID   string `json:"locationId,omitempty"`
	LocationName string `json:"locationName,omitempty"`
	Description  string `json:"description,omitempty"`
	OpeningHours string `json:"openingHours,omitempty"`
}

type Item struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Quantity    int64  `json:"quantity"`
}

type createRequest struct {
	Pickup         PlaceRef  `json:"pickup"`
	Delivery       PlaceRef  `json:"delivery"`
	Items          []Item    `json:"items"`
	OfferedCredits int64     `json:"offeredCredits"`
	ExpiresAt      time.Time `json:"expiresAt"`
}

type Order struct {
	DeliveredAt           *time.Time `json:"deliveredAt,omitempty"`
	CompletionRequestedAt *time.Time `json:"completionRequestedAt,omitempty"`
	ID                    string     `json:"id"`
	RequesterID           string     `json:"requesterId"`
	Pickup                Place      `json:"pickup"`
	Delivery              Place      `json:"delivery"`
	Items                 []Item     `json:"items"`
	OfferedCredits        int64      `json:"offeredCredits"`
	ExpiresAt             time.Time  `json:"expiresAt"`
	Status                string     `json:"status"`
	CreatedAt             time.Time  `json:"createdAt"`
	CourierID             *string    `json:"courierId"`
	Version               int64      `json:"version"`
	CancellationReason    string     `json:"cancellationReason,omitempty"`
}

func (b *createRequest) validate(now time.Time) error {
	if err := b.Pickup.validate(); err != nil {
		return err
	}
	if err := b.Delivery.validate(); err != nil {
		return err
	}
	if b.Pickup == b.Delivery {
		return problem(400, "places_must_differ", "Pickup and delivery must be different places.")
	}
	if len(b.Items) == 0 {
		return problem(400, "invalid_items", "Add at least one item.")
	}
	for i := range b.Items {
		if err := b.Items[i].validate(); err != nil {
			return err
		}
	}
	if b.OfferedCredits <= 0 {
		return problem(400, "invalid_offer", "Offered credits must be a positive whole number.")
	}
	if b.ExpiresAt.IsZero() || !b.ExpiresAt.After(now) {
		return problem(400, "invalid_expiry", "Expiry must be later than the current Singapore time.")
	}
	return nil
}

func (p *PlaceRef) validate() error {
	if (p.Type != "location" && p.Type != "supplier") || p.ID == "" || p.ID != strings.TrimSpace(p.ID) || !validPlainText(p.ID) {
		return problem(400, "invalid_place", "Choose a campus location or active supplier.")
	}
	if p.Type == "supplier" {
		var id pgtype.UUID
		if len(p.ID) != 36 || id.Scan(p.ID) != nil || !id.Valid {
			return invalidPlace()
		}
		p.ID = strings.ToLower(p.ID)
	}
	return nil
}

func (i *Item) validate() error {
	if strings.TrimSpace(i.Name) == "" || !validText(i.Name, true) || !validText(i.Description, true) || i.Quantity <= 0 {
		return problem(400, "invalid_items", "Every item needs a name and a positive whole-number quantity.")
	}
	return nil
}

func validPlainText(value string) bool {
	return validText(value, false)
}

func validText(value string, multiline bool) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) && !(multiline && (r == '\n' || r == '\r' || r == '\t')) {
			return false
		}
	}
	return true
}

func duplicateKey(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		// Canonical representative of Unicode's simple case-fold equivalence
		// class (also handles final sigma and long s).
		smallest := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			if next < smallest {
				smallest = next
			}
		}
		return smallest
	}, value)
}

func (b createRequest) fingerprint() string {
	b.ExpiresAt = b.ExpiresAt.UTC()
	return requestFingerprint(b)
}

// Also used to recognize exact retries recorded before canonicalization.
func requestFingerprint(b createRequest) string {
	data, _ := json.Marshal(b)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func orderID(requesterID, key string) string {
	sum := sha256.Sum256([]byte(requesterID + "\x00" + key))
	return "order-" + hex.EncodeToString(sum[:])
}
