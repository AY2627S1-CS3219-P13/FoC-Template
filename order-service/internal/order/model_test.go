package order

import (
	"testing"
	"time"
)

func TestDuplicateKeyRemovesWhitespaceAndIgnoresCase(t *testing.T) {
	if duplicateKey("Milk Tea") != duplicateKey(" \tMILKtea\n") {
		t.Fatal("expected names to match")
	}
	if duplicateKey("Milk Tea") == duplicateKey("Milk Coffee") {
		t.Fatal("expected different names not to match")
	}
}

func TestCreateValidation(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.FixedZone("Asia/Singapore", 8*60*60))
	valid := createRequest{
		Pickup: PlaceRef{Type: "location", ID: "com2"}, Delivery: PlaceRef{Type: "supplier", ID: "11111111-1111-4111-8111-111111111111"},
		Items: []Item{{Name: "Tea", Quantity: 1}}, OfferedCredits: 1, ExpiresAt: now.Add(time.Minute),
	}
	if err := valid.validate(now); err != nil {
		t.Fatal(err)
	}
	tests := []createRequest{
		{Pickup: valid.Pickup, Delivery: valid.Pickup, Items: valid.Items, OfferedCredits: 1, ExpiresAt: valid.ExpiresAt},
		{Pickup: valid.Pickup, Delivery: valid.Delivery, OfferedCredits: 1, ExpiresAt: valid.ExpiresAt},
		{Pickup: valid.Pickup, Delivery: valid.Delivery, Items: []Item{{Name: " ", Quantity: 1}}, OfferedCredits: 1, ExpiresAt: valid.ExpiresAt},
		{Pickup: valid.Pickup, Delivery: valid.Delivery, Items: []Item{{Name: "Tea", Quantity: 0}}, OfferedCredits: 1, ExpiresAt: valid.ExpiresAt},
		{Pickup: valid.Pickup, Delivery: valid.Delivery, Items: valid.Items, OfferedCredits: 0, ExpiresAt: valid.ExpiresAt},
		{Pickup: valid.Pickup, Delivery: valid.Delivery, Items: valid.Items, OfferedCredits: 1, ExpiresAt: now},
	}
	for i := range tests {
		if err := tests[i].validate(now); err == nil {
			t.Fatalf("case %d passed", i)
		}
	}
}

func TestNoBusinessItemLimit(t *testing.T) {
	now := time.Now()
	items := make([]Item, 1000)
	for i := range items {
		items[i] = Item{Name: "item", Description: "description", Quantity: int64(i + 1)}
	}
	body := createRequest{Pickup: PlaceRef{Type: "location", ID: "a"}, Delivery: PlaceRef{Type: "location", ID: "b"}, Items: items, OfferedCredits: 1, ExpiresAt: now.Add(time.Hour)}
	if err := body.validate(now); err != nil {
		t.Fatal(err)
	}
}
