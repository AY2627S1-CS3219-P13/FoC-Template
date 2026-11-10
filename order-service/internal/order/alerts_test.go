package order

import (
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	schedule "foc/schedule-order"
)

// fakePushService plays a browser push service. Each endpoint path selects the
// response status; delivered messages are decrypted with the device's keys.
type fakePushService struct {
	server   *httptest.Server
	mu       sync.Mutex
	devices  map[string]*testDevice
	received []pushReceived
}

type testDevice struct {
	endpoint string
	key      *ecdh.PrivateKey
	auth     []byte
	status   int
}

type pushReceived struct {
	device  string
	header  http.Header
	payload map[string]any
}

func newFakePushService(t *testing.T) *fakePushService {
	t.Helper()
	f := &fakePushService{devices: map[string]*testDevice{}}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		name := strings.TrimPrefix(r.URL.Path, "/push/")
		device := f.devices[name]
		if device == nil {
			w.WriteHeader(410)
			return
		}
		if device.status != 201 {
			w.WriteHeader(device.status)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		if err := json.Unmarshal(decryptPush(t, body, device.key, device.auth), &payload); err != nil {
			t.Error(err)
		}
		f.received = append(f.received, pushReceived{name, r.Header.Clone(), payload})
		w.WriteHeader(201)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakePushService) device(t *testing.T, name string) *testDevice {
	t.Helper()
	key, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	d := &testDevice{endpoint: f.server.URL + "/push/" + name, key: key, auth: auth, status: 201}
	f.mu.Lock()
	f.devices[name] = d
	f.mu.Unlock()
	return d
}

func (f *fakePushService) messages() []pushReceived {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pushReceived{}, f.received...)
}

func (d *testDevice) subscription() map[string]any {
	return map[string]any{"endpoint": d.endpoint, "keys": map[string]string{
		"p256dh": base64.RawURLEncoding.EncodeToString(d.key.PublicKey().Bytes()),
		"auth":   base64.RawURLEncoding.EncodeToString(d.auth),
	}}
}

// alertsApp enables push with a real VAPID key, the real internal catalogue
// client against the fake Supplier, and an allowlist that also admits the fake push service.
func alertsApp(t *testing.T) (*App, *fakePushService) {
	t.Helper()
	a, fakes := testApp(t)
	a.auth = actorAuth{}
	push := newFakePushService(t)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	a.push = newPushService(Config{VAPIDKey: key, VAPIDSubject: "https://github.com/AY2627S1-CS3219-P13/FoC-Template",
		SupplierInternalURL: fakes.server.URL, SupplierInternalToken: testSupplierInternalToken}, &http.Client{Timeout: 2 * time.Second})
	a.push.endpoint = func(raw string) (*url.URL, string, bool) {
		if strings.HasPrefix(raw, push.server.URL+"/push/") {
			u, err := url.Parse(raw)
			return u, push.server.URL, err == nil
		}
		return parsePushEndpoint(raw)
	}
	return a, push
}

func enableAlerts(t *testing.T, a *App, token, location string, radius int, devices ...*testDevice) {
	t.Helper()
	for _, d := range devices {
		assertStatus(t, request(t, a, "PUT", "/api/v1/orders/alerts/subscription", token, "", d.subscription()), 204)
	}
	savePreferences(t, a, token, alertPreference{true, location, radius})
}

// savePreferences replaces the caller's whole alert list.
func savePreferences(t *testing.T, a *App, token string, preferences ...alertPreference) {
	t.Helper()
	assertStatus(t, request(t, a, "PUT", "/api/v1/orders/alerts", token, "", map[string]any{"preferences": append([]alertPreference{}, preferences...)}), 200)
}

func runAlerts(t *testing.T, a *App) {
	t.Helper()
	consume(t, a, nearbyAlertConsumer)
	if err := a.SendPushAlerts(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func deliveries(t *testing.T, a *App, where string, args ...any) int {
	t.Helper()
	return workflowCount(t, a, "SELECT count(*) FROM push_deliveries WHERE "+where, args...)
}

func TestAlertsAPIValidatesPreferencesAndSubscriptions(t *testing.T) {
	a, push := alertsApp(t)
	var settings struct {
		PushAvailable  bool              `json:"pushAvailable"`
		VAPIDPublicKey string            `json:"vapidPublicKey"`
		Preferences    []alertPreference `json:"preferences"`
		Devices        int               `json:"devices"`
	}
	got := request(t, a, "GET", "/api/v1/orders/alerts", "courier", "", nil)
	assertStatus(t, got, 200)
	if json.Unmarshal(got.Body.Bytes(), &settings) != nil || !settings.PushAvailable || len(b64url(t, settings.VAPIDPublicKey)) != 65 ||
		!strings.Contains(got.Body.String(), `"preferences":[]`) || settings.Devices != 0 {
		t.Fatalf("unexpected settings %s", got.Body.String())
	}
	library := `{"enabled":true,"locationId":"central-library","radiusMeters":250}`
	for body, code := range map[string]string{
		`{"preferences":[{"enabled":true,"locationId":"central-library","radiusMeters":300}]}`:       "invalid_radius",
		`{"preferences":[{"enabled":true,"locationId":"central-library","radiusMeters":500}]}`:       "invalid_radius",
		`{"preferences":[{"enabled":true,"locationId":"central-library","radiusMeters":-1}]}`:        "invalid_radius",
		`{"preferences":[{"enabled":true,"locationId":"nowhere","radiusMeters":250}]}`:               "invalid_location",
		`{"preferences":[` + library + `,{"enabled":true,"locationId":"annex","radiusMeters":250}]}`: "invalid_location",
		`{"preferences":[{"locationId":"central-library","radiusMeters":250}]}`:                      "invalid_alert_preference",
		`{"preferences":[{"enabled":true,"locationId":"central-library"}]}`:                          "invalid_alert_preference",
		`{"preferences":[{"enabled":true,"radiusMeters":250}]}`:                                      "invalid_alert_preference",
		`{}`: "invalid_alert_preference",
		`{"preferences":[{"enabled":true,"locationId":"central-library","radiusMeters":250,"x":1}]}`: "invalid_json",
		library: "invalid_json", // the single-preference body from before migration 015
		`{"preferences":[` + library + `,{"enabled":false,"locationId":"central-library","radiusMeters":0}]}`:                                                                                                   "duplicate_alert_location",
		`{"preferences":[` + library + `,{"enabled":true,"locationId":"com2","radiusMeters":0},{"enabled":true,"locationId":"utown","radiusMeters":0},{"enabled":true,"locationId":"annex","radiusMeters":0}]}`: "too_many_alert_preferences",
	} {
		response := rawRequest(a, "PUT", "/api/v1/orders/alerts", "courier", "", []byte(body))
		if response.Code != 400 || !strings.Contains(response.Body.String(), code) {
			t.Fatalf("%s: got %d %s", body, response.Code, response.Body.String())
		}
	}
	if workflowCount(t, a, "SELECT count(*) FROM nearby_alert_preferences") != 0 {
		t.Fatal("a rejected list was saved")
	}
	phone := push.device(t, "phone")
	enableAlerts(t, a, "courier", "central-library", 250, phone)
	want := []alertPreference{{true, "com2", 0}, {false, "utown", 100}, {true, "central-library", 250}}
	savePreferences(t, a, "courier", want...)
	got = request(t, a, "GET", "/api/v1/orders/alerts", "courier", "", nil)
	if json.Unmarshal(got.Body.Bytes(), &settings) != nil || len(settings.Preferences) != 3 || settings.Devices != 1 {
		t.Fatalf("preferences not saved: %s", got.Body.String())
	}
	for i := range want {
		if settings.Preferences[i] != want[i] {
			t.Fatalf("preferences not saved in order: %s", got.Body.String())
		}
	}
	if strings.Contains(got.Body.String(), phone.endpoint) {
		t.Fatal("endpoint returned by the API")
	}
	// Saving keeps the cooldown of a location that stays in the list.
	workflowSQL(t, a, "UPDATE nearby_alert_preferences SET last_alerted_at=clock_timestamp() WHERE location_id='central-library'")
	savePreferences(t, a, "courier", alertPreference{false, "central-library", 100}, alertPreference{true, "utown", 0})
	if workflowCount(t, a, "SELECT count(*) FROM nearby_alert_preferences WHERE location_id='central-library' AND position=1 AND last_alerted_at IS NOT NULL") != 1 ||
		workflowCount(t, a, "SELECT count(*) FROM nearby_alert_preferences WHERE location_id='utown' AND last_alerted_at IS NULL") != 1 {
		t.Fatal("cooldowns not carried across a save")
	}
	// An empty list removes every alert location.
	savePreferences(t, a, "courier")
	if workflowCount(t, a, "SELECT count(*) FROM nearby_alert_preferences WHERE user_id=$1", courierUser) != 0 {
		t.Fatal("empty list kept preferences")
	}

	bad := phone.subscription()
	for _, change := range []func(map[string]any){
		func(s map[string]any) { s["endpoint"] = "http://fcm.googleapis.com/fcm/send/x" },
		func(s map[string]any) { s["endpoint"] = "https://example.com/push" },
		func(s map[string]any) {
			s["keys"] = map[string]string{"p256dh": "AAAA", "auth": "AAAAAAAAAAAAAAAAAAAAAA"}
		},
		func(s map[string]any) {
			s["keys"] = map[string]string{"p256dh": bad["keys"].(map[string]string)["p256dh"], "auth": "AAAA"}
		},
	} {
		body := phone.subscription()
		change(body)
		response := request(t, a, "PUT", "/api/v1/orders/alerts/subscription", "courier", "", body)
		if response.Code != 400 || !strings.Contains(response.Body.String(), "invalid_subscription") {
			t.Fatalf("invalid subscription accepted: %d %s", response.Code, response.Body.String())
		}
	}
	// Without the trusted Origin, mutations are refused before authentication.
	r := httptest.NewRequest("PUT", "/api/v1/orders/alerts/subscription", strings.NewReader("{}"))
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(&http.Cookie{Name: "foc_session", Value: "courier"})
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	assertStatus(t, w, 403)

	// At most five devices: a sixth replaces the oldest.
	for i := range 6 {
		assertStatus(t, request(t, a, "PUT", "/api/v1/orders/alerts/subscription", "courier", "", push.device(t, "d"+strconv.Itoa(i)).subscription()), 204)
	}
	if workflowCount(t, a, "SELECT count(*) FROM push_subscriptions WHERE user_id=$1", courierUser) != maxPushDevices ||
		workflowCount(t, a, "SELECT count(*) FROM push_subscriptions WHERE endpoint=$1", phone.endpoint) != 0 {
		t.Fatal("device limit not enforced oldest-first")
	}
	// A shared browser belongs to the account that enabled alerts on it last.
	shared := push.device(t, "shared")
	assertStatus(t, request(t, a, "PUT", "/api/v1/orders/alerts/subscription", "courier", "", shared.subscription()), 204)
	assertStatus(t, request(t, a, "PUT", "/api/v1/orders/alerts/subscription", "other", "", shared.subscription()), 204)
	if workflowCount(t, a, "SELECT count(*) FROM push_subscriptions WHERE endpoint=$1 AND user_id=$2", shared.endpoint, otherUser) != 1 {
		t.Fatal("shared browser did not move")
	}
	// Removing a device only touches the caller's own row.
	assertStatus(t, request(t, a, "DELETE", "/api/v1/orders/alerts/subscription", "courier", "", map[string]string{"endpoint": shared.endpoint}), 204)
	if workflowCount(t, a, "SELECT count(*) FROM push_subscriptions WHERE endpoint=$1", shared.endpoint) != 1 {
		t.Fatal("another account's device removed")
	}
	assertStatus(t, request(t, a, "DELETE", "/api/v1/orders/alerts/subscription", "other", "", map[string]string{"endpoint": shared.endpoint}), 204)
	if workflowCount(t, a, "SELECT count(*) FROM push_subscriptions WHERE endpoint=$1", shared.endpoint) != 0 {
		t.Fatal("own device not removed")
	}

	a.push = nil
	got = request(t, a, "GET", "/api/v1/orders/alerts", "courier", "", nil)
	settings.PushAvailable, settings.VAPIDPublicKey = true, "" // JSON null leaves a field unchanged
	if json.Unmarshal(got.Body.Bytes(), &settings) != nil || settings.PushAvailable || settings.VAPIDPublicKey != "" {
		t.Fatalf("unconfigured push advertised: %s", got.Body.String())
	}
	response := request(t, a, "PUT", "/api/v1/orders/alerts", "courier", "", map[string]any{"preferences": []alertPreference{{true, "central-library", 250}}})
	if response.Code != 503 || !strings.Contains(response.Body.String(), "push_unavailable") {
		t.Fatalf("got %d %s", response.Code, response.Body.String())
	}
}

func TestNearbyAlertsReachOnlyCouriersInRange(t *testing.T) {
	a, push := alertsApp(t)
	courierPhone, courierLaptop := push.device(t, "courier-phone"), push.device(t, "courier-laptop")
	enableAlerts(t, a, "courier", "com2", 0, courierPhone, courierLaptop)        // the COM2 pickup itself
	enableAlerts(t, a, "other", "central-library", 250, push.device(t, "other")) // COM2 is ~315 m away
	enableAlerts(t, a, "requester", "com2", 250, push.device(t, "requester"))    // never alerted about their own errand

	o := createdForTest(t, a, "nearby-1")
	runAlerts(t, a)
	got := push.messages()
	if len(got) != 2 || deliveries(t, a, "status='sent'") != 2 {
		t.Fatalf("want one alert per courier device, got %d", len(got))
	}
	for _, m := range got {
		if !strings.HasPrefix(m.device, "courier-") {
			t.Fatal("alert reached", m.device)
		}
		h := m.header
		ttl, _ := strconv.Atoi(h.Get("TTL"))
		if h.Get("Content-Encoding") != "aes128gcm" || h.Get("Urgency") != "high" || len(h.Get("Topic")) != 32 ||
			ttl < 3500 || ttl > 3600 || !strings.HasPrefix(h.Get("Authorization"), "vapid t=") {
			t.Fatalf("unexpected push headers %v", h)
		}
		if m.payload["orderId"] != o.ID || m.payload["url"] != "/orders?view=open&order="+url.QueryEscape(o.ID) ||
			m.payload["body"] != "Campus Cafe (COM2) · at your alert location. Open Help a friend to see all nearby errands." {
			t.Fatalf("unexpected payload %+v", m.payload)
		}
	}

	// The two-minute cooldown holds back a second errand.
	second := createdForTest(t, a, "nearby-2")
	runAlerts(t, a)
	if len(push.messages()) != 2 || deliveries(t, a, "order_id=$1", second.ID) != 0 {
		t.Fatal("cooldown did not hold back the second errand")
	}
	// Replaying the first event cannot alert a device twice.
	workflowSQL(t, a, "DELETE FROM workflow_receipts WHERE consumer_name=$1 AND event_id=$2", nearbyAlertConsumer, o.ID+":1")
	workflowSQL(t, a, "UPDATE nearby_alert_preferences SET last_alerted_at=NULL")
	runAlerts(t, a)
	if len(push.messages()) != 2 || deliveries(t, a, "event_id=$1", o.ID+":1") != 2 {
		t.Fatal("replayed event alerted again", len(push.messages()))
	}
	// Other lifecycle events never alert.
	mustAction(t, a, o.ID, "courier", "accept", "accepted")
	workflowSQL(t, a, "UPDATE nearby_alert_preferences SET last_alerted_at=NULL")
	runAlerts(t, a)
	if len(push.messages()) != 2 {
		t.Fatal("non-creation event alerted")
	}
	// Switching the preference off stops them.
	savePreferences(t, a, "courier", alertPreference{false, "com2", 0})
	createdForTest(t, a, "nearby-3")
	runAlerts(t, a)
	if len(push.messages()) != 2 {
		t.Fatal("disabled preference alerted")
	}
}

type coordinatesFunc func(context.Context, string) (map[string]*Coordinate, error)

func (f coordinatesFunc) Coordinates(ctx context.Context, session string) (map[string]*Coordinate, error) {
	return f(ctx, session)
}

// A courier with several preferences gets one alert per errand, from the
// closest matching one, and each preference has its own cooldown and switch.
func TestNearbyAlertsUseEachPreference(t *testing.T) {
	a, push := alertsApp(t)
	// Terrace is ~56 m from COM2; the fake Supplier catalogue does not list it.
	catalogue := a.push.locations
	a.push.locations = coordinatesFunc(func(ctx context.Context, session string) (map[string]*Coordinate, error) {
		places, err := catalogue.Coordinates(ctx, session)
		if err == nil {
			places["terrace"] = &Coordinate{Latitude: 1.294425, Longitude: 103.774153, Name: "Terrace"}
		}
		return places, err
	})
	enableAlerts(t, a, "courier", "central-library", 0, push.device(t, "courier"))
	savePreferences(t, a, "courier", alertPreference{true, "central-library", 250}, alertPreference{true, "com2", 0})
	workflowSQL(t, a, "INSERT INTO nearby_alert_preferences(user_id,position,enabled,location_id,radius_meters) VALUES($1,3,true,'terrace',100)", courierUser)
	cooling := func() []string {
		var positions []string
		for p := 1; p <= 3; p++ {
			if workflowCount(t, a, "SELECT count(*) FROM nearby_alert_preferences WHERE user_id=$1 AND position=$2 AND last_alerted_at IS NOT NULL", courierUser, p) == 1 {
				positions = append(positions, strconv.Itoa(p))
			}
		}
		return positions
	}

	// COM2 (0 m) and Terrace (~56 m) both match; Central Library (~315 m) does not.
	first := createdForTest(t, a, "several-1")
	runAlerts(t, a)
	got := push.messages()
	if len(got) != 1 || got[0].payload["orderId"] != first.ID ||
		got[0].payload["body"] != "Campus Cafe (COM2) · at your alert location. Open Help a friend to see all nearby errands." {
		t.Fatalf("want one alert from the closest preference, got %+v", got)
	}
	if c := cooling(); strings.Join(c, ",") != "2" {
		t.Fatal("cooldown started on the wrong preferences", c)
	}
	// COM2 is cooling down, so Terrace alerts and names itself.
	second := createdForTest(t, a, "several-2")
	runAlerts(t, a)
	got = push.messages()
	if len(got) != 2 || got[1].payload["orderId"] != second.ID ||
		got[1].payload["body"] != "Campus Cafe (COM2) · about 60 m from your alert at Terrace. Open Help a friend to see all nearby errands." {
		t.Fatalf("second preference did not alert: %+v", got)
	}
	if c := cooling(); strings.Join(c, ",") != "2,3" {
		t.Fatal("cooldown started on the wrong preferences", c)
	}
	// Both matching preferences are cooling down.
	third := createdForTest(t, a, "several-3")
	runAlerts(t, a)
	if len(push.messages()) != 2 || deliveries(t, a, "order_id=$1", third.ID) != 0 {
		t.Fatal("alerted while every matching preference was cooling down")
	}
	// A switched-off preference never matches, even out of cooldown.
	workflowSQL(t, a, "UPDATE nearby_alert_preferences SET last_alerted_at=NULL")
	workflowSQL(t, a, "UPDATE nearby_alert_preferences SET enabled=false WHERE location_id IN ('com2','terrace')")
	fourth := createdForTest(t, a, "several-4")
	runAlerts(t, a)
	if len(push.messages()) != 2 || deliveries(t, a, "order_id=$1", fourth.ID) != 0 {
		t.Fatal("switched-off preference alerted")
	}
}

func TestNearbyAlertsSkipBusyCouriersAndStaleOrders(t *testing.T) {
	a, push := alertsApp(t)
	enableAlerts(t, a, "courier", "com2", 0, push.device(t, "courier"))
	for i := range 3 {
		busy := createdForTest(t, a, "busy-"+strconv.Itoa(i))
		mustAction(t, a, busy.ID, "courier", "accept", "accepted")
	}
	createdForTest(t, a, "while-busy")
	runAlerts(t, a)
	if deliveries(t, a, "true") != 0 {
		t.Fatal("courier at the three-order limit was alerted")
	}
	if workflowCount(t, a, "SELECT count(*) FROM nearby_alert_preferences WHERE last_alerted_at IS NOT NULL") != 0 {
		t.Fatal("skipped courier used their cooldown")
	}
	workflowSQL(t, a, "UPDATE orders SET status='cancelled' WHERE courier_id=$1", courierUser)
	stale := createdForTest(t, a, "stale")
	workflowSQL(t, a, "UPDATE orders SET created_at=clock_timestamp()-interval '11 minutes' WHERE id=$1", stale.ID)
	runAlerts(t, a)
	if deliveries(t, a, "order_id=$1", stale.ID) != 0 {
		t.Fatal("errand older than ten minutes alerted after a backlog")
	}
	fresh := createdForTest(t, a, "fresh")
	runAlerts(t, a)
	if deliveries(t, a, "order_id=$1 AND status='sent'", fresh.ID) != 1 {
		t.Fatal("free courier not alerted")
	}
}

func TestNearbyAlertsWaitForCatalogueAndIgnoreUnconfiguredPush(t *testing.T) {
	a, push := alertsApp(t)
	enableAlerts(t, a, "courier", "com2", 0, push.device(t, "courier"))
	locations := a.push.locations
	a.push.locations = &CatalogueClient{BaseURL: "http://127.0.0.1:1", InternalToken: testSupplierInternalToken, Client: &http.Client{Timeout: time.Second}}
	o := createdForTest(t, a, "catalogue-down")
	if err := a.ConsumeWorkflow(context.Background(), nearbyAlertConsumer); err == nil {
		t.Fatal("pass ran without coordinates")
	}
	if workflowCount(t, a, "SELECT count(*) FROM workflow_receipts WHERE consumer_name=$1", nearbyAlertConsumer) != 0 {
		t.Fatal("event acknowledged without matching")
	}
	a.push.locations = locations
	runAlerts(t, a)
	if deliveries(t, a, "order_id=$1 AND status='sent'", o.ID) != 1 {
		t.Fatal("alert lost after catalogue recovery")
	}
	a.push = nil
	createdForTest(t, a, "unconfigured")
	consume(t, a, nearbyAlertConsumer)
	if workflowCount(t, a, "SELECT count(*) FROM order_events e WHERE NOT EXISTS(SELECT 1 FROM workflow_receipts r WHERE r.consumer_name=$1 AND r.event_id=e.event_id)", nearbyAlertConsumer) != 0 {
		t.Fatal("events not acknowledged while push is unconfigured")
	}
}

func TestPushSenderRetriesRemovesAndSkips(t *testing.T) {
	a, push := alertsApp(t)
	busy, gone, rejected, denied := push.device(t, "busy"), push.device(t, "gone"), push.device(t, "rejected"), push.device(t, "denied")
	busy.status, gone.status, rejected.status, denied.status = 503, 410, 400, 403
	enableAlerts(t, a, "courier", "com2", 0, busy, gone, rejected, denied)
	createdForTest(t, a, "sender")
	runAlerts(t, a)
	if workflowCount(t, a, "SELECT count(*) FROM push_subscriptions WHERE endpoint=$1", gone.endpoint) != 0 {
		t.Fatal("410 did not remove the subscription")
	}
	if workflowCount(t, a, "SELECT count(*) FROM push_deliveries d JOIN push_subscriptions s ON s.id=d.subscription_id WHERE s.endpoint=$1 AND d.status='failed' AND d.last_error='push_rejected'", rejected.endpoint) != 1 ||
		workflowCount(t, a, "SELECT count(*) FROM push_deliveries d JOIN push_subscriptions s ON s.id=d.subscription_id WHERE s.endpoint=$1 AND d.status='failed' AND d.last_error='push_unauthorized'", denied.endpoint) != 1 {
		t.Fatal("permanent failures not recorded")
	}
	retrying := "EXISTS(SELECT 1 FROM push_subscriptions s WHERE s.id=subscription_id AND s.endpoint=$1)"
	if deliveries(t, a, "status='pending' AND attempts=1 AND next_attempt_at>clock_timestamp() AND "+retrying, busy.endpoint) != 1 {
		t.Fatal("503 not scheduled for retry")
	}
	for range pushMaxAttempts {
		workflowSQL(t, a, "UPDATE push_deliveries SET next_attempt_at=clock_timestamp() WHERE status='pending'")
		if err := a.SendPushAlerts(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if deliveries(t, a, "status='failed' AND attempts=$2 AND last_error='push_unavailable' AND "+retrying, busy.endpoint, pushMaxAttempts) != 1 {
		t.Fatal("retries did not give up after five attempts")
	}

	// An errand taken before its alert is sent is skipped, not announced.
	workflowSQL(t, a, "UPDATE nearby_alert_preferences SET last_alerted_at=NULL")
	busy.status = 201
	taken := createdForTest(t, a, "taken")
	consume(t, a, nearbyAlertConsumer)
	mustAction(t, a, taken.ID, "other", "accept", "accepted")
	if err := a.SendPushAlerts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if deliveries(t, a, "order_id=$1 AND status='skipped'", taken.ID) != 3 || len(push.messages()) != 0 {
		t.Fatal("accepted errand was announced")
	}

	// Finished records are kept for seven days; pending jobs are never pruned.
	workflowSQL(t, a, "UPDATE push_deliveries SET created_at=clock_timestamp()-interval '8 days'")
	workflowSQL(t, a, "UPDATE push_deliveries SET status='pending' WHERE order_id=$1 AND EXISTS(SELECT 1 FROM push_subscriptions s WHERE s.id=subscription_id AND s.endpoint=$2)", taken.ID, busy.endpoint)
	if err := a.CleanupPushDeliveries(context.Background()); err != nil {
		t.Fatal(err)
	}
	if deliveries(t, a, "true") != 1 || deliveries(t, a, "status='pending'") != 1 {
		t.Fatal("cleanup removed the wrong records")
	}
	status, err := a.workflowStatus(context.Background())
	if err != nil || status["pushDeliveries"].(map[string]int64)["pending"] != 1 {
		t.Fatal("push deliveries missing from status", status, err)
	}
}

// Migration 013 marks every existing event as handled, so a deploy never
// announces errands that were already open.
func TestNearbyAlertsMigrationNeverAnnouncesExistingErrands(t *testing.T) {
	a, push := alertsApp(t)
	existing := createdForTest(t, a, "before-upgrade")
	workflowSQL(t, a, "DROP TABLE push_deliveries, push_subscriptions, nearby_alert_preferences")
	workflowSQL(t, a, "DELETE FROM workflow_receipts WHERE consumer_name=$1", nearbyAlertConsumer)
	workflowSQL(t, a, "DELETE FROM schema_migrations WHERE name IN ('013_nearby_alerts.sql','014_alert_radius.sql','015_alert_preferences.sql')")
	if err := Migrate(context.Background(), a.db); err != nil {
		t.Fatal(err)
	}
	enableAlerts(t, a, "courier", "com2", 0, push.device(t, "courier"))
	runAlerts(t, a)
	if deliveries(t, a, "order_id=$1", existing.ID) != 0 || len(push.messages()) != 0 {
		t.Fatal("existing errand announced after upgrade")
	}
	createdForTest(t, a, "after-upgrade")
	runAlerts(t, a)
	if len(push.messages()) != 1 {
		t.Fatal("new errand not announced after upgrade")
	}
}

// Scheduled requests reach Order only when a courier accepts them: the import
// commits OrderCreated and acceptance together, so it must never alert.
func TestScheduledImportNeverAlertsNearbyCouriers(t *testing.T) {
	a, push := alertsApp(t)
	ctx := context.Background()
	if err := schedule.Migrate(ctx, a.db); err != nil {
		t.Fatal(err)
	}
	a.backgroundCatalogue = resolverFunc(func(_ context.Context, r PlaceRef, _ string) (Place, error) {
		return Place{Type: r.Type, ID: r.ID, Name: r.ID}, nil
	})
	enableAlerts(t, a, "courier", "com2", 0, push.device(t, "courier"))
	start := time.Date(time.Now().Year()+1, 1, 2, 9, 0, 0, 0, time.FixedZone("SGT", 28800))
	request, _ := json.Marshal(map[string]any{"pickup": PlaceRef{"location", "com2"}, "delivery": PlaceRef{"location", "utown"}, "items": []Item{{Name: "Tea", Quantity: 1}}, "offeredCredits": 10, "windowStart": start})
	data, _ := json.Marshal(map[string]any{"id": "scheduled-alert-test", "requesterId": testUserID, "status": "scheduled", "expiresAt": start.Add(30 * time.Minute), "windowStart": start})
	workflowSQL(t, a, `INSERT INTO schedules(id,requester_id,idempotency_key,fingerprint,request,order_data,state,courier_id,transfer_id,generation)
 VALUES('scheduled-alert-test',$1,'alerts','fingerprint',$2,$3,'transfer_pending',$4,'alert-event',1)`, testUserID, request, data, otherUser)
	workflowSQL(t, a, `INSERT INTO schedule_events(event_id,schedule_id,event_type) VALUES('alert-event','scheduled-alert-test','ScheduledOrderAcceptanceRequested')`)
	if err := schedule.New(a.db, schedule.Config{}, a.log, a.PrepareScheduledEvent).ConsumeEvents(ctx); err != nil {
		t.Fatal(err)
	}
	if workflowCount(t, a, "SELECT count(*) FROM orders WHERE id='scheduled-alert-test' AND status='accepted'") != 1 {
		t.Fatal("scheduled request was not imported")
	}
	runAlerts(t, a)
	if deliveries(t, a, "true") != 0 || len(push.messages()) != 0 {
		t.Fatal("an already accepted scheduled order alerted nearby couriers")
	}
	if workflowCount(t, a, "SELECT count(*) FROM order_events e WHERE NOT EXISTS(SELECT 1 FROM workflow_receipts r WHERE r.consumer_name=$1 AND r.event_id=e.event_id)", nearbyAlertConsumer) != 0 {
		t.Fatal("imported events left unacknowledged")
	}
}
