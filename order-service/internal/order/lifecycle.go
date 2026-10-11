package order

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// The request is the unit of progress. There are no item-level transitions or
// generic status-setting endpoint. Actor permissions belong to each command.
func forwardTransition(from, to string) bool {
	switch from {
	case "open":
		return to == "accepted" || to == "cancelled" || to == "expired"
	case "accepted":
		return to == "picked up" || to == "cancelled" || to == "expired"
	case "picked up":
		return to == "delivered" || to == "cancelled" || to == "expired"
	case "delivered":
		return to == "completed" || to == "dispute"
	case "dispute":
		return to == "completed" || to == "not completed"
	default:
		return false
	}
}

func checkChange(before, after Order) error {
	if before.ID != after.ID || (before.Status != after.Status && !forwardTransition(before.Status, after.Status)) {
		return problem(409, "status_conflict", "This errand cannot make that state transition.")
	}
	// Assignment is permanent history, even after its workload slot is released.
	if !sameCourier(before.CourierID, after.CourierID) && !(before.Status == "open" && after.Status == "accepted" && before.CourierID == nil && after.CourierID != nil && *after.CourierID != before.RequesterID) {
		return problem(409, "assignment_conflict", "An errand cannot be reassigned.")
	}
	return nil
}

func recordAction(ctx context.Context, tx pgx.Tx, o Order, action, user string, received time.Time) error {
	_, err := tx.Exec(ctx, `INSERT INTO order_actions(order_id,action,actor_id,received_at,order_version)
 VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, o.ID, action, user, received, o.Version)
	return err
}

func actionRecorded(ctx context.Context, tx pgx.Tx, id, action, user string) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM order_actions WHERE order_id=$1 AND action=$2 AND actor_id=$3)", id, action, user).Scan(&exists)
	return exists, err
}

func sameTime(a, b *time.Time) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && a.Equal(*b))
}
