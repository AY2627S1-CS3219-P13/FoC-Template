package order

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const pricingSafeInteger int64 = 9007199254740991

type pricingDraft struct {
	ID       string `json:"id"`
	Revision *int64 `json:"revision"`
}
type quoteRequest struct {
	Draft             pricingDraft `json:"draft"`
	Pickup            PlaceRef     `json:"pickup"`
	Delivery          PlaceRef     `json:"delivery"`
	OrderExpiresAt    *time.Time   `json:"orderExpiresAt,omitempty"`
	TotalItemQuantity *int64       `json:"totalItemQuantity,omitempty"`
}
type quoteResponse struct {
	QuoteID            string       `json:"quoteId"`
	Draft              pricingDraft `json:"draft"`
	RecommendedCredits int64        `json:"recommendedCredits"`
	PricingMode        string       `json:"pricingMode"`
	QuotedAt           time.Time    `json:"quotedAt"`
	ValidUntil         time.Time    `json:"validUntil"`
	ReleaseID          string       `json:"releaseId"`
	ContextSnapshotID  string       `json:"contextSnapshotId"`
	Warnings           []string     `json:"warnings"`
}

func canonicalUUID(value string) bool {
	var id pgtype.UUID
	return len(value) == 36 && value == strings.ToLower(value) && id.Scan(value) == nil && id.Valid
}

// Token parsing catches duplicate object keys at every depth before unknown
// fields are discarded. Unknown fields never enter the downstream fingerprint.
func quoteJSON(d *json.Decoder) (any, error) {
	token, err := d.Token()
	if err != nil {
		return nil, err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return token, nil
	}
	switch delimiter {
	case '{':
		object := map[string]any{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return nil, err
			}
			name, ok := key.(string)
			if !ok {
				return nil, fmt.Errorf("invalid key")
			}
			if _, exists := object[name]; exists {
				return nil, fmt.Errorf("duplicate key")
			}
			value, err := quoteJSON(d)
			if err != nil {
				return nil, err
			}
			object[name] = value
		}
		_, err = d.Token()
		return object, err
	case '[':
		values := []any{}
		for d.More() {
			value, err := quoteJSON(d)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		_, err = d.Token()
		return values, err
	}
	return nil, fmt.Errorf("invalid delimiter")
}

func (a *App) quoteBudget(next endpoint) endpoint {
	return func(w http.ResponseWriter, r *http.Request) error {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		return next(w, r.WithContext(ctx))
	}
}

func quoteUnavailable() error {
	return problem(503, "quote_unavailable", "Suggestion unavailable. You can enter your own offered credits.")
}

func (a *App) priceQuote(w http.ResponseWriter, r *http.Request) error {
	if a.cfg.PricingURL == "" {
		return quoteUnavailable()
	}
	if r.URL.RawQuery != "" {
		return problem(400, "invalid_request", "Quote query parameters are not supported.")
	}
	key := r.Header.Get("Idempotency-Key")
	if len(r.Header.Values("Idempotency-Key")) != 1 || !canonicalUUID(key) {
		return problem(400, "invalid_request", "Send one UUID Idempotency-Key.")
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return problem(415, "unsupported_media_type", "Send Content-Type: application/json.")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.UseNumber()
	value, err := quoteJSON(decoder)
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return problem(413, "request_too_large", "Quote request exceeds 16 KiB.")
	}
	if err != nil || decoder.Decode(new(any)) != io.EOF {
		return problem(400, "invalid_request", "Send one valid quote object without duplicate keys.")
	}
	object, ok := value.(map[string]any)
	if !ok || object == nil {
		return problem(400, "invalid_request", "Send a quote object.")
	}
	for _, field := range []string{"draft", "pickup", "delivery", "orderExpiresAt", "totalItemQuantity"} {
		if v, exists := object[field]; exists && v == nil {
			return problem(400, "invalid_request", "Quote fields cannot be null.")
		}
	}
	for field, names := range map[string][]string{"draft": {"id", "revision"}, "pickup": {"type", "id"}, "delivery": {"type", "id"}} {
		nested, ok := object[field].(map[string]any)
		if !ok {
			return problem(400, "invalid_request", "Draft and places are required.")
		}
		for _, name := range names {
			if nested[name] == nil {
				return problem(400, "invalid_request", "Draft and place fields are required.")
			}
		}
	}
	raw, _ := json.Marshal(object)
	var body quoteRequest
	if json.Unmarshal(raw, &body) != nil || !canonicalUUID(body.Draft.ID) || body.Draft.Revision == nil || *body.Draft.Revision < 0 || *body.Draft.Revision > pricingSafeInteger || (body.OrderExpiresAt != nil && body.OrderExpiresAt.IsZero()) || body.Pickup.validate() != nil || body.Delivery.validate() != nil || body.Pickup == body.Delivery || body.TotalItemQuantity != nil && (*body.TotalItemQuantity <= 0 || *body.TotalItemQuantity > pricingSafeInteger) {
		return problem(400, "invalid_request", "Choose valid places, expiry, draft and item quantity.")
	}
	identity := r.Context().Value(identityKey{}).(Identity)
	// Fixed, database-owned minute buckets bound work across replicas without
	// retaining browser credentials. Authentication is still checked on every call.
	var count int
	err = a.db.QueryRow(r.Context(), `INSERT INTO pricing_quote_limits(user_id,window_start,requests)
 VALUES($1,date_trunc('minute',clock_timestamp()),1)
 ON CONFLICT(user_id) DO UPDATE SET window_start=EXCLUDED.window_start,
 requests=CASE WHEN pricing_quote_limits.window_start=EXCLUDED.window_start THEN pricing_quote_limits.requests+1 ELSE 1 END
 RETURNING requests`, identity.UserID).Scan(&count)
	if err != nil {
		return quoteUnavailable()
	}
	if count > 60 {
		w.Header().Set("Retry-After", "60")
		return problem(429, "rate_limited", "Too many suggestions. Please wait before refreshing.")
	}
	if body.OrderExpiresAt != nil {
		expiry := body.OrderExpiresAt.UTC()
		body.OrderExpiresAt = &expiry
	}
	raw, _ = json.Marshal(body)
	ctx, cancel := context.WithTimeout(r.Context(), 1500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.PricingURL+"/internal/v1/price-quotes", bytes.NewReader(raw))
	if err != nil {
		return quoteUnavailable()
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.PricingToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", scopedQuoteKey(a.cfg.PricingToken, identity.UserID, key))
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return quoteUnavailable()
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 16*1024+1))
	if err != nil || len(responseBody) > 16*1024 {
		return quoteUnavailable()
	}
	if response.StatusCode != 200 {
		var failure struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(responseBody, &failure) != nil {
			return quoteUnavailable()
		}
		status, code := response.StatusCode, failure.Error.Code
		allowed := map[string]int{"invalid_request": 400, "idempotency_conflict": 409, "unsupported_place": 422, "rate_limited": 429, "context_unavailable": 503, "quote_unavailable": 503, "quote_recording_unavailable": 503, "quote_pending": 503}
		if allowed[code] != status {
			return quoteUnavailable()
		}
		if status == 429 {
			if seconds, err := strconv.Atoi(response.Header.Get("Retry-After")); err == nil && seconds > 0 && seconds <= 3600 {
				w.Header().Set("Retry-After", strconv.Itoa(seconds))
			}
		}
		return problem(status, code, "Suggestion unavailable. Check your details or enter your own offered credits.")
	}
	var quote quoteResponse
	if json.Unmarshal(responseBody, &quote) != nil || !canonicalUUID(quote.QuoteID) || quote.Draft.ID != body.Draft.ID || quote.Draft.Revision == nil || *quote.Draft.Revision != *body.Draft.Revision || quote.RecommendedCredits <= 0 || quote.RecommendedCredits > pricingSafeInteger || quote.PricingMode == "" || quote.ReleaseID == "" || quote.ContextSnapshotID == "" || quote.Warnings == nil || quote.QuotedAt.IsZero() || !quote.ValidUntil.After(quote.QuotedAt) || quote.ValidUntil.After(quote.QuotedAt.Add(120*time.Second)) || (body.OrderExpiresAt != nil && quote.ValidUntil.After(*body.OrderExpiresAt)) {
		return quoteUnavailable()
	}
	writeJSON(w, 200, quote)
	return nil
}

// Stable keyed derivation hides user identity and scopes browser keys. Keep the
// quote credential stable across ordinary image releases for replay continuity.
func scopedQuoteKey(secret, user, key string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(user + "\x00" + key))
	sum := mac.Sum(nil)[:16]
	sum[6] = (sum[6] & 15) | 80
	sum[8] = (sum[8] & 63) | 128
	value := hex.EncodeToString(sum)
	return value[:8] + "-" + value[8:12] + "-" + value[12:16] + "-" + value[16:20] + "-" + value[20:]
}

func (a *App) pricingContext(w http.ResponseWriter, r *http.Request) error {
	if !validSecret(a.cfg.PricingSourceToken) || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+a.cfg.PricingSourceToken)) != 1 {
		return problem(401, "invalid_service_credential", "A valid service credential is required.")
	}
	if r.URL.RawQuery != "" {
		return problem(400, "invalid_request", "Query parameters are not supported.")
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	tx, err := a.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer rollback(tx)
	var asOf time.Time
	if err = tx.QueryRow(ctx, "SELECT transaction_timestamp()").Scan(&asOf); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT CASE WHEN pickup->>'type'='location' THEN pickup->>'id' ELSE pickup->>'locationId' END AS location_id,
 count(*) FILTER(WHERE status='open' AND expires_at>$1),
 count(*) FILTER(WHERE created_at >= $1::timestamptz-interval '15 minutes' AND created_at<$1)
 FROM orders WHERE window_start IS NULL AND ((status='open' AND expires_at>$1) OR (created_at >= $1::timestamptz-interval '15 minutes' AND created_at<$1))
 GROUP BY 1 ORDER BY 1 LIMIT 10001`, asOf)
	if err != nil {
		return err
	}
	type area struct {
		LocationID string `json:"locationId"`
		Open       int64  `json:"openRequestCount"`
		Created    int64  `json:"createdOrderCount"`
	}
	areas := []area{}
	for rows.Next() {
		var item area
		if err = rows.Scan(&item.LocationID, &item.Open, &item.Created); err != nil {
			rows.Close()
			return err
		}
		areas = append(areas, item)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	if len(areas) > 10000 {
		return problem(503, "snapshot_too_large", "Pricing snapshot is too large.")
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"schemaVersion": 1, "snapshotId": "orders-" + asOf.UTC().Format(time.RFC3339Nano), "asOf": asOf.UTC(), "windowStart": asOf.UTC().Add(-15 * time.Minute), "windowEnd": asOf.UTC(), "areas": areas})
	return nil
}
