package schedule

import (
	"context"
	"net/url"
	"testing"
)

func TestAcceptanceEventAndPendingStateAreAtomic(t *testing.T) {
	a, _ := fixture(t)
	ctx := context.Background()
	id := create(t, a, "event-atomicity")
	_, err := a.db.Exec(ctx, `CREATE FUNCTION fail_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'event write failed'; END $$;
 CREATE TRIGGER fail_event BEFORE INSERT ON schedule_events FOR EACH ROW EXECUTE FUNCTION fail_event()`)
	if err != nil {
		t.Fatal(err)
	}
	if w := call(t, a, "POST", "/api/v1/scheduled-orders/"+id+"/actions", "courier", "", map[string]string{"action": "accept"}); w.Code != 500 {
		t.Fatal(w.Code, w.Body.String())
	}
	var state string
	var generation int
	if err = a.db.QueryRow(ctx, "SELECT state,generation FROM schedules WHERE id=$1", id).Scan(&state, &generation); err != nil || state != "scheduled" || generation != 0 {
		t.Fatal(state, generation, err)
	}
}

func TestLegacyImportPreservesPendingWorkAndRefusesOverwrite(t *testing.T) {
	source, _ := fixture(t)
	target, _ := fixture(t)
	ctx := context.Background()
	id := create(t, source, "legacy")
	sourceURL, _ := url.Parse(source.db.Config().ConnString())
	query := sourceURL.Query()
	query.Set("search_path", source.db.Config().ConnConfig.RuntimeParams["search_path"])
	sourceURL.RawQuery = query.Encode()
	if w := call(t, source, "POST", "/api/v1/scheduled-orders/"+id+"/actions", "courier", "", map[string]string{"action": "accept"}); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	// JSON values must survive copy without float64 decoding/rounding.
	if _, err := source.db.Exec(ctx, `UPDATE schedules SET request=jsonb_set(request,'{offeredCredits}','9007199254740993') WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := ImportLegacy(ctx, target.db, sourceURL.String()); err != nil {
		t.Fatal(err)
	}
	var exact string
	if err := target.db.QueryRow(ctx, "SELECT request->>'offeredCredits' FROM schedules WHERE id=$1", id).Scan(&exact); err != nil || exact != "9007199254740993" {
		t.Fatal("JSON integer changed", exact, err)
	}
	var state, transfer string
	var events int
	if err := target.db.QueryRow(ctx, "SELECT state,transfer_id FROM schedules WHERE id=$1", id).Scan(&state, &transfer); err != nil || state != "transfer_pending" || transfer == "" {
		t.Fatal(state, transfer, err)
	}
	if err := target.db.QueryRow(ctx, "SELECT count(*) FROM schedule_events WHERE event_id=$1", transfer).Scan(&events); err != nil || events != 1 {
		t.Fatal(events, err)
	}
	if err := ImportLegacy(ctx, target.db, sourceURL.String()); err == nil {
		t.Fatal("nonempty target overwritten")
	}
	if err := source.db.QueryRow(ctx, "SELECT state FROM schedules WHERE id=$1", id).Scan(&state); err != nil || state != "transfer_pending" {
		t.Fatal("source changed", state, err)
	}
}
