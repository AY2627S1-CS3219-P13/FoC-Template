package order

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// Nearby errand alerts: couriers keep up to three preferences, each a campus
// location and radius (0 m for that location only, 100 m or 250 m) with its own
// on/off switch; each new open errand whose pickup is within range of one
// becomes a Web Push notification.
// See NEARBY-ALERTS.md for the design.

const (
	nearbyAlertConsumer = "workflow-nearby-alerts"
	maxAlertPreferences = 3
	maxPushDevices      = 5
	pushClaimBatch      = 20
	pushMaxAttempts     = 5
)

// pushService exists only when VAPID is configured.
type pushService struct {
	signer *vapidSigner
	client *http.Client
	// endpoint validates browser-supplied endpoints; tests substitute a fake push service.
	endpoint func(string) (*url.URL, string, bool)
	// locations reads the campus catalogue with Order's own service token.
	locations locationLocator
}

func newPushService(cfg Config, catalogue *http.Client) *pushService {
	if cfg.VAPIDKey == nil {
		return nil
	}
	signer, err := newVAPIDSigner(cfg.VAPIDKey, cfg.VAPIDSubject, time.Now)
	if err != nil {
		return nil
	}
	return &pushService{
		signer:    signer,
		client:    &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		endpoint:  parsePushEndpoint,
		locations: &CatalogueClient{BaseURL: cfg.SupplierInternalURL, InternalToken: cfg.SupplierInternalToken, Client: catalogue},
	}
}

func pushUnavailable() error {
	return problem(503, "push_unavailable", "Nearby alerts are not available on this server.")
}

type alertPreference struct {
	Enabled      bool   `json:"enabled"`
	LocationID   string `json:"locationId"`
	RadiusMeters int    `json:"radiusMeters"`
}

func validRadius(meters int) bool { return meters == 0 || meters == 100 || meters == 250 }

func (a *App) alertSettings(w http.ResponseWriter, r *http.Request) error {
	body := map[string]any{"pushAvailable": a.push != nil, "vapidPublicKey": nil, "preferences": []alertPreference{}, "devices": 0}
	if a.push != nil {
		body["vapidPublicKey"] = a.push.signer.publicKey
	}
	rows, err := a.db.Query(r.Context(), "SELECT enabled,location_id,radius_meters FROM nearby_alert_preferences WHERE user_id=$1 ORDER BY position", actor(r))
	if err != nil {
		return err
	}
	preferences := []alertPreference{}
	for rows.Next() {
		var p alertPreference
		if err = rows.Scan(&p.Enabled, &p.LocationID, &p.RadiusMeters); err != nil {
			break
		}
		preferences = append(preferences, p)
	}
	rows.Close()
	if err = errors.Join(err, rows.Err()); err != nil {
		return err
	}
	body["preferences"] = preferences
	var devices int
	if err = a.db.QueryRow(r.Context(), "SELECT count(*) FROM push_subscriptions WHERE user_id=$1", actor(r)).Scan(&devices); err != nil {
		return err
	}
	body["devices"] = devices
	writeJSON(w, 200, body)
	return nil
}

// saveAlertPreferences replaces the caller's whole preference list. A location
// that stays in the list keeps its cooldown, so saving never resets it.
func (a *App) saveAlertPreferences(w http.ResponseWriter, r *http.Request) error {
	if a.push == nil {
		return pushUnavailable()
	}
	var body struct {
		Preferences []struct {
			Enabled      *bool  `json:"enabled"`
			LocationID   string `json:"locationId"`
			RadiusMeters *int   `json:"radiusMeters"`
		} `json:"preferences"`
	}
	if err := readJSON(w, r, &body); err != nil {
		return err
	}
	if body.Preferences == nil {
		return problem(400, "invalid_alert_preference", "Send preferences: a list of enabled, locationId and radiusMeters.")
	}
	if len(body.Preferences) > maxAlertPreferences {
		return problem(400, "too_many_alert_preferences", "Choose at most three alert locations.")
	}
	saved := make([]alertPreference, 0, len(body.Preferences))
	seen := make(map[string]bool)
	for _, p := range body.Preferences {
		if p.Enabled == nil || p.LocationID == "" || len(p.LocationID) > 128 || p.RadiusMeters == nil {
			return problem(400, "invalid_alert_preference", "Send enabled, locationId and radiusMeters for each alert location.")
		}
		if !validRadius(*p.RadiusMeters) {
			return problem(400, "invalid_radius", "Choose this location only (0), 100 or 250 metres.")
		}
		if seen[p.LocationID] {
			return problem(400, "duplicate_alert_location", "Each campus location can appear only once in your alerts.")
		}
		seen[p.LocationID] = true
		saved = append(saved, alertPreference{*p.Enabled, p.LocationID, *p.RadiusMeters})
	}
	if len(saved) > 0 {
		cookie, _ := r.Cookie(a.cfg.SessionCookie)
		session := ""
		if cookie != nil {
			session = cookie.Value
		}
		coordinates, err := a.locator.Coordinates(r.Context(), session)
		if err != nil {
			return err
		}
		for _, p := range saved {
			if coordinates[p.LocationID] == nil {
				return problem(400, "invalid_location", "Choose a campus location with a known position.")
			}
		}
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err = tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", "alert-preferences:"+actor(r)); err != nil {
		return err
	}
	// Matching locks preference rows while it runs, so this waits for a pass in progress.
	rows, err := tx.Query(r.Context(), "DELETE FROM nearby_alert_preferences WHERE user_id=$1 RETURNING location_id,last_alerted_at", actor(r))
	if err != nil {
		return err
	}
	cooldowns := make(map[string]*time.Time)
	for rows.Next() {
		var location string
		var last *time.Time
		if err = rows.Scan(&location, &last); err != nil {
			break
		}
		cooldowns[location] = last
	}
	rows.Close()
	if err = errors.Join(err, rows.Err()); err != nil {
		return err
	}
	for i, p := range saved {
		if _, err = tx.Exec(r.Context(), `INSERT INTO nearby_alert_preferences(user_id,position,enabled,location_id,radius_meters,last_alerted_at)
 VALUES($1,$2,$3,$4,$5,$6)`, actor(r), i+1, p.Enabled, p.LocationID, p.RadiusMeters, cooldowns[p.LocationID]); err != nil {
			return err
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"preferences": saved})
	return nil
}

type subscriptionRequest struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

func invalidSubscription() error {
	return problem(400, "invalid_subscription", "This browser's push subscription is not supported.")
}

// saveSubscription registers this browser for the caller. A browser belongs to
// one account at a time: moving it to another account drops the old pending jobs.
func (a *App) saveSubscription(w http.ResponseWriter, r *http.Request) error {
	if a.push == nil {
		return pushUnavailable()
	}
	var body subscriptionRequest
	if err := readJSON(w, r, &body); err != nil {
		return err
	}
	if _, _, ok := a.push.endpoint(body.Endpoint); !ok {
		return invalidSubscription()
	}
	point, secret, ok := decodePushKeys(body.Keys.P256dh, body.Keys.Auth)
	if !ok {
		return invalidSubscription()
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err = tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", "push-devices:"+actor(r)); err != nil {
		return err
	}
	if _, err = tx.Exec(r.Context(), "DELETE FROM push_subscriptions WHERE endpoint=$1 AND user_id<>$2", body.Endpoint, actor(r)); err != nil {
		return err
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO push_subscriptions(user_id,endpoint,p256dh,auth) VALUES($1,$2,$3,$4)
 ON CONFLICT(endpoint) DO UPDATE SET p256dh=EXCLUDED.p256dh,auth=EXCLUDED.auth,created_at=clock_timestamp()`, actor(r), body.Endpoint, point, secret); err != nil {
		return err
	}
	if _, err = tx.Exec(r.Context(), `DELETE FROM push_subscriptions WHERE user_id=$1 AND id NOT IN
 (SELECT id FROM push_subscriptions WHERE user_id=$1 ORDER BY created_at DESC,id DESC LIMIT $2)`, actor(r), maxPushDevices); err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (a *App) deleteSubscription(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Endpoint string `json:"endpoint"`
	}
	if err := readJSON(w, r, &body); err != nil {
		return err
	}
	if body.Endpoint == "" || len(body.Endpoint) > 1024 {
		return invalidSubscription()
	}
	if _, err := a.db.Exec(r.Context(), "DELETE FROM push_subscriptions WHERE endpoint=$1 AND user_id=$2", body.Endpoint, actor(r)); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// alertSnapshot reads campus coordinates once per consumer pass, outside any
// transaction. Without push configured, events are acknowledged with no effect.
func (a *App) alertSnapshot(ctx context.Context) (map[string]*Coordinate, error) {
	if a.push == nil {
		return nil, nil
	}
	coordinates, err := a.push.locations.Coordinates(ctx, "")
	if err != nil {
		return nil, errors.New("campus coordinates unavailable")
	}
	return coordinates, nil
}

// matchNearby turns one OrderCreated event into push jobs for nearby couriers.
// It runs inside the consumer transaction, so jobs and the receipt commit together.
func matchNearby(ctx context.Context, tx pgx.Tx, e domainEvent, coordinates map[string]*Coordinate) error {
	if coordinates == nil || e.Type != "OrderCreated" {
		return nil
	}
	var raw []byte
	var requester string
	var fresh bool
	err := tx.QueryRow(ctx, `SELECT pickup,requester_id::text,
 status='open' AND expires_at>clock_timestamp() AND created_at>clock_timestamp()-interval '10 minutes'
 FROM orders WHERE id=$1`, e.OrderID).Scan(&raw, &requester, &fresh)
	if err != nil {
		return err
	}
	var pickup Place
	if json.Unmarshal(raw, &pickup) != nil {
		return errDomain
	}
	origin := coordinates[pickupLocationID(Order{Pickup: pickup})]
	if !fresh || origin == nil {
		return nil
	}
	// Each enabled preference outside its own cooldown is a candidate. Couriers at
	// the three-active-order acceptance limit could not take it.
	rows, err := tx.Query(ctx, `SELECT p.user_id::text,p.position,p.location_id,p.radius_meters FROM nearby_alert_preferences p
 WHERE p.enabled AND p.user_id<>$1::uuid
 AND (p.last_alerted_at IS NULL OR p.last_alerted_at<=clock_timestamp()-interval '2 minutes')
 AND EXISTS(SELECT 1 FROM push_subscriptions s WHERE s.user_id=p.user_id)
 AND (SELECT count(*) FROM orders o WHERE o.courier_id=p.user_id AND o.status IN ('accepted','picked up'))<3
 ORDER BY p.user_id,p.position FOR UPDATE OF p`, requester)
	if err != nil {
		return err
	}
	// One alert per courier: the closest matching preference, the first on a tie.
	type match struct {
		position int16
		distance int64
		location string
	}
	best := make(map[string]match)
	var users []string
	for rows.Next() {
		var user, location string
		var position int16
		var radius int64
		if err = rows.Scan(&user, &position, &location, &radius); err != nil {
			break
		}
		from := coordinates[location]
		if from == nil {
			continue
		}
		d := distanceMeters(*from, *origin)
		if d > radius {
			continue
		}
		current, found := best[user]
		if !found {
			users = append(users, user)
		}
		if !found || d < current.distance {
			best[user] = match{position, d, from.Name}
		}
	}
	rows.Close()
	if err = errors.Join(err, rows.Err()); err != nil || len(users) == 0 {
		return err
	}
	positions, distances, names := make([]int16, len(users)), make([]int64, len(users)), make([]string, len(users))
	for i, user := range users {
		m := best[user]
		positions[i], distances[i], names[i] = m.position, m.distance, m.location
	}
	// Only the preference that produced the alert starts its cooldown.
	if _, err = tx.Exec(ctx, `UPDATE nearby_alert_preferences p SET last_alerted_at=clock_timestamp()
 FROM unnest($1::text[]::uuid[],$2::smallint[]) AS m(u,position) WHERE p.user_id=m.u AND p.position=m.position`, users, positions); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO push_deliveries(event_id,subscription_id,order_id,distance_meters,alert_location)
 SELECT $1,s.id,$2,m.d,m.name FROM unnest($3::text[]::uuid[],$4::bigint[],$5::text[]) AS m(u,d,name) JOIN push_subscriptions s ON s.user_id=m.u
 ON CONFLICT DO NOTHING`, e.ID, e.OrderID, users, distances, names)
	return err
}

type pushJob struct {
	eventID        string
	subscriptionID int64
	orderID        string
	distance       int64
	alertLocation  string
	attempts       int
}

// SendPushAlerts claims due jobs with a 30-second lease and sends them in
// parallel. Delivery is at least once; Topic and the notification tag collapse
// a rare resend after a crash.
func (a *App) SendPushAlerts(ctx context.Context) error {
	if a.push == nil {
		return nil
	}
	rows, err := a.db.Query(ctx, `WITH due AS (SELECT event_id,subscription_id FROM push_deliveries
 WHERE status='pending' AND next_attempt_at<=clock_timestamp() ORDER BY next_attempt_at LIMIT $1 FOR UPDATE SKIP LOCKED)
 UPDATE push_deliveries d SET next_attempt_at=clock_timestamp()+interval '30 seconds' FROM due
 WHERE d.event_id=due.event_id AND d.subscription_id=due.subscription_id
 RETURNING d.event_id,d.subscription_id,d.order_id,d.distance_meters,d.alert_location,d.attempts`, pushClaimBatch)
	if err != nil {
		return err
	}
	var jobs []pushJob
	for rows.Next() {
		var j pushJob
		if err = rows.Scan(&j.eventID, &j.subscriptionID, &j.orderID, &j.distance, &j.alertLocation, &j.attempts); err != nil {
			break
		}
		jobs = append(jobs, j)
	}
	rows.Close()
	if err = errors.Join(err, rows.Err()); err != nil {
		return err
	}
	var wg sync.WaitGroup
	failures := make([]error, len(jobs))
	for i, job := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			failures[i] = a.sendPush(ctx, job)
		}()
	}
	wg.Wait()
	return errors.Join(failures...)
}

func (a *App) sendPush(ctx context.Context, job pushJob) error {
	var endpoint string
	var point, secret, rawPickup []byte
	var open bool
	var expires time.Time
	err := a.db.QueryRow(ctx, `SELECT s.endpoint,s.p256dh,s.auth,o.pickup,o.status='open' AND o.expires_at>clock_timestamp(),o.expires_at
 FROM push_subscriptions s JOIN orders o ON o.id=$2 WHERE s.id=$1`, job.subscriptionID, job.orderID).Scan(&endpoint, &point, &secret, &rawPickup, &open, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // the device was removed; its jobs went with it
	}
	if err != nil {
		return err
	}
	if !open {
		return a.finishPush(ctx, job, "skipped", "")
	}
	target, audience, ok := a.push.endpoint(endpoint)
	if !ok {
		return a.finishPush(ctx, job, "failed", "push_rejected")
	}
	var pickup Place
	if json.Unmarshal(rawPickup, &pickup) != nil {
		return a.finishPush(ctx, job, "failed", "push_rejected")
	}
	payload, err := json.Marshal(alertPayload(job.orderID, pickup, job.distance, job.alertLocation))
	if err != nil {
		return err
	}
	body, err := sealPush(payload, point, secret)
	if err != nil {
		return a.finishPush(ctx, job, "failed", "push_rejected")
	}
	authorization, err := a.push.signer.authorization(audience)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	ttl := int64(time.Until(expires).Seconds())
	if ttl < 0 {
		ttl = 0
	}
	topic := sha256.Sum256([]byte(job.orderID))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("TTL", strconv.FormatInt(ttl, 10))
	req.Header.Set("Urgency", "high")
	req.Header.Set("Topic", base64.RawURLEncoding.EncodeToString(topic[:24]))
	req.Header.Set("Authorization", authorization)
	response, err := a.push.client.Do(req)
	if err != nil {
		return a.retryPush(ctx, job, 0)
	}
	_ = response.Body.Close()
	switch status := response.StatusCode; {
	case status == 201 || status == 202 || status == 200:
		return a.finishPush(ctx, job, "sent", "")
	case status == 404 || status == 410:
		_, err = a.db.Exec(ctx, "DELETE FROM push_subscriptions WHERE id=$1", job.subscriptionID)
		return err
	case status == 429 || status >= 500:
		after, _ := strconv.Atoi(response.Header.Get("Retry-After"))
		return a.retryPush(ctx, job, after)
	case status == 401 || status == 403:
		return a.finishPush(ctx, job, "failed", "push_unauthorized")
	default:
		return a.finishPush(ctx, job, "failed", "push_rejected")
	}
}

func (a *App) finishPush(ctx context.Context, job pushJob, status, category string) error {
	var last *string
	if category != "" {
		last = &category
		a.log.Warn("push delivery failed", "category", category)
	}
	_, err := a.db.Exec(ctx, `UPDATE push_deliveries SET status=$3,attempts=attempts+1,last_error=$4
 WHERE event_id=$1 AND subscription_id=$2 AND status='pending'`, job.eventID, job.subscriptionID, status, last)
	return err
}

// retryPush backs off min(60, 2^attempts) seconds, or the push service's
// Retry-After capped at 60, and gives up after five attempts.
func (a *App) retryPush(ctx context.Context, job pushJob, retryAfter int) error {
	if job.attempts+1 >= pushMaxAttempts {
		return a.finishPush(ctx, job, "failed", "push_unavailable")
	}
	delay := min(60, 1<<min(job.attempts+1, 6))
	if retryAfter > 0 {
		delay = min(60, retryAfter)
	}
	_, err := a.db.Exec(ctx, `UPDATE push_deliveries SET attempts=attempts+1,last_error='push_unavailable',
 next_attempt_at=clock_timestamp()+$3::integer*interval '1 second' WHERE event_id=$1 AND subscription_id=$2 AND status='pending'`, job.eventID, job.subscriptionID, delay)
	return err
}

// alertPayload is the decrypted notification content. It names the place, the
// distance and the courier's matching alert location only: never items, credits
// or user IDs. Jobs queued before migration 015 have no alert location.
func alertPayload(orderID string, pickup Place, distance int64, alertLocation string) map[string]any {
	place := pickup.Name
	if pickup.Type == "supplier" && pickup.LocationName != "" {
		place += " (" + pickup.LocationName + ")"
	}
	where := alertDistance(distance) + " from your alert location"
	if alertLocation != "" {
		where = alertDistance(distance) + " from your alert at " + alertLocation
	}
	if distance == 0 {
		where = "at your alert location"
	}
	return map[string]any{
		"v": 1, "orderId": orderID, "title": "New errand nearby",
		"body": place + " · " + where + ". Open Help a friend to see all nearby errands.",
		"url":  "/orders?view=open&order=" + url.QueryEscape(orderID),
	}
}

// alertDistance matches formatDistance in web/core.mjs.
func alertDistance(meters int64) string {
	switch {
	case meters < 50:
		return "under 50 m"
	case meters < 1000:
		return fmt.Sprintf("about %d m", (meters+5)/10*10)
	default:
		return fmt.Sprintf("about %.1f km", float64(meters)/1000)
	}
}

// CleanupPushDeliveries removes finished delivery records after seven days.
func (a *App) CleanupPushDeliveries(ctx context.Context) error {
	_, err := a.db.Exec(ctx, "DELETE FROM push_deliveries WHERE status<>'pending' AND created_at<clock_timestamp()-interval '7 days'")
	return err
}

func (a *App) pushStatus(ctx context.Context) (map[string]int64, error) {
	var pending, sent, failed int64
	err := a.db.QueryRow(ctx, `SELECT count(*) FILTER(WHERE status='pending'),count(*) FILTER(WHERE status='sent'),
 count(*) FILTER(WHERE status='failed') FROM push_deliveries`).Scan(&pending, &sent, &failed)
	return map[string]int64{"pending": pending, "sent": sent, "failed": failed}, err
}
