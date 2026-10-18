package order

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Private, versioned transition data. Explicit nulls are part of schema 1;
// items, place descriptions, financial amounts and credentials never enter it.
type domainData struct {
	PreviousStatus        *string    `json:"previousStatus"`
	Status                string     `json:"status"`
	RequesterID           string     `json:"requesterId"`
	CourierID             *string    `json:"courierId"`
	ExpiresAt             time.Time  `json:"expiresAt"`
	DeliveredAt           *time.Time `json:"deliveredAt"`
	CompletionRequestedAt *time.Time `json:"completionRequestedAt"`
	CancellationReason    *string    `json:"cancellationReason"`
}

type domainEvent struct {
	ID, OrderID, Type string
	Version           int64
	Schema            int
	OccurredAt        time.Time
	Data              domainData
}

var errDomain = errors.New("invalid_domain_event")

func domainType(before *Order, after Order) string {
	if before == nil {
		return "OrderCreated"
	}
	if before.Status == after.Status {
		if before.CompletionRequestedAt == nil && after.CompletionRequestedAt != nil {
			return "OrderCompletionRequested"
		}
		return "OrderDetailsChanged"
	}
	return map[string]string{"accepted": "OrderAccepted", "picked up": "OrderPickedUp", "delivered": "OrderDelivered", "dispute": "OrderDisputed", "completed": "OrderCompleted", "cancelled": "OrderCancelled", "expired": "OrderExpired", "not completed": "OrderNotCompleted"}[after.Status]
}

func recordDomainEvent(ctx context.Context, tx pgx.Tx, before *Order, o Order, public string) error {
	data := domainData{Status: o.Status, RequesterID: o.RequesterID, CourierID: o.CourierID, ExpiresAt: o.ExpiresAt, DeliveredAt: o.DeliveredAt, CompletionRequestedAt: o.CompletionRequestedAt}
	if before != nil {
		data.PreviousStatus = &before.Status
	}
	if o.Status == "cancelled" {
		data.CancellationReason = &o.CancellationReason
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	kind := domainType(before, o)
	// Use the same validation on writers and consumers. The timestamp is generated
	// once by PostgreSQL in the event row, not reconstructed by a later reader.
	if err = validateDomain(domainEvent{ID: o.ID + ":" + strconv.FormatInt(o.Version, 10), OrderID: o.ID, Type: kind, Version: o.Version, Schema: 1, Data: data}); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO order_events(event_id,order_id,order_version,event_type,domain_type,domain_schema_version,domain_payload)
 VALUES($1,$2,$3,$4,$5,1,$6)`, o.ID+":"+strconv.FormatInt(o.Version, 10), o.ID, o.Version, public, kind, payload)
	return err
}

func decodeDomain(raw []byte, e *domainEvent) error {
	// Reject missing explicit nullable fields, unknown fields, malformed data and
	// future schemas. Preserve poison work for diagnosis instead of acknowledging it.
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 8 {
		return errDomain
	}
	for _, key := range []string{"previousStatus", "status", "requesterId", "courierId", "expiresAt", "deliveredAt", "completionRequestedAt", "cancellationReason"} {
		if _, ok := fields[key]; !ok {
			return errDomain
		}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&e.Data) != nil || dec.Decode(new(any)) != io.EOF {
		return errDomain
	}
	return validateDomain(*e)
}

func validUser(id string) bool {
	var u pgtype.UUID
	return len(id) == 36 && u.Scan(id) == nil && u.Valid
}
func validateDomain(e domainEvent) error {
	d := e.Data
	if e.Schema != 1 || e.Version < 1 || e.ID != e.OrderID+":"+strconv.FormatInt(e.Version, 10) || !validUser(d.RequesterID) || d.ExpiresAt.IsZero() {
		return errDomain
	}
	if d.CourierID != nil && (!validUser(*d.CourierID) || *d.CourierID == d.RequesterID) {
		return errDomain
	}
	expected := map[string]string{"OrderCreated": "open", "OrderAccepted": "accepted", "OrderPickedUp": "picked up", "OrderDelivered": "delivered", "OrderCompletionRequested": "delivered", "OrderDisputed": "dispute", "OrderCompleted": "completed", "OrderCancelled": "cancelled", "OrderExpired": "expired", "OrderNotCompleted": "not completed"}
	if e.Type == "OrderDetailsChanged" {
		if d.PreviousStatus == nil || *d.PreviousStatus != d.Status {
			return errDomain
		}
		switch d.Status {
		case "open", "accepted", "picked up", "delivered", "dispute", "completed", "cancelled", "expired", "not completed":
		default:
			return errDomain
		}
	} else if status, ok := expected[e.Type]; !ok || status != d.Status {
		return errDomain
	}
	if e.Type == "OrderCreated" {
		if d.PreviousStatus != nil || e.Version != 1 || d.CourierID != nil {
			return errDomain
		}
	} else {
		if d.PreviousStatus == nil {
			return errDomain
		}
		if e.Type == "OrderCompletionRequested" {
			if *d.PreviousStatus != "delivered" || d.CompletionRequestedAt == nil {
				return errDomain
			}
		} else if e.Type != "OrderDetailsChanged" && !forwardTransition(*d.PreviousStatus, d.Status) {
			return errDomain
		}
	}
	switch d.Status {
	case "accepted", "picked up", "delivered", "dispute", "completed", "not completed":
		if d.CourierID == nil {
			return errDomain
		}
	}
	// Legacy snapshot repair validates delivery timestamps separately. Writers
	// always supply the database-generated value for new delivery transitions.
	if e.Type == "OrderDelivered" || e.Type == "OrderCompletionRequested" {
		if d.DeliveredAt == nil {
			return errDomain
		}
	}
	if e.Type == "OrderCancelled" && d.CancellationReason == nil {
		return errDomain
	}
	return nil
}
