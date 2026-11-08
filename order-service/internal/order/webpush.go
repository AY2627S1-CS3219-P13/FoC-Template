package order

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Web Push with the standard library only: RFC 8291 message encryption
// (aes128gcm, RFC 8188) and RFC 8292 VAPID application-server identification.

const pushRecordSize = 4096

var errPushKeys = errors.New("invalid push subscription keys")

// encryptPush encrypts one push message for a browser subscription. The
// ephemeral key and salt are parameters so the RFC 8291 vector can be reproduced.
func encryptPush(plaintext, uaPublic, authSecret []byte, ephemeral *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	if len(authSecret) != 16 || len(salt) != 16 {
		return nil, errPushKeys
	}
	browserKey, err := ecdh.P256().NewPublicKey(uaPublic)
	if err != nil {
		return nil, errPushKeys
	}
	secret, err := ephemeral.ECDH(browserKey)
	if err != nil {
		return nil, errPushKeys
	}
	asPublic := ephemeral.PublicKey().Bytes()
	keyInfo := "WebPush: info\x00" + string(uaPublic) + string(asPublic)
	ikm, err := hkdf.Key(sha256.New, secret, authSecret, keyInfo, 32)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	// One record: plaintext, the final-record delimiter 0x02, and no padding.
	record := append(append([]byte{}, plaintext...), 0x02)
	if len(record)+gcm.Overhead() > pushRecordSize {
		return nil, errors.New("push payload too large")
	}
	header := make([]byte, 0, 21+len(asPublic))
	header = append(header, salt...)
	header = binary.BigEndian.AppendUint32(header, pushRecordSize)
	header = append(header, byte(len(asPublic)))
	header = append(header, asPublic...)
	return gcm.Seal(header, nonce, record, nil), nil
}

// sealPush encrypts with a fresh ephemeral key and salt, as required per message.
func sealPush(plaintext, uaPublic, authSecret []byte) ([]byte, error) {
	ephemeral, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err = rand.Read(salt); err != nil {
		return nil, err
	}
	return encryptPush(plaintext, uaPublic, authSecret, ephemeral, salt)
}

// vapidSigner signs and caches one JWT per push-service origin.
type vapidSigner struct {
	key       *ecdsa.PrivateKey
	publicKey string // base64url uncompressed point, the browser's applicationServerKey
	subject   string
	now       func() time.Time
	mu        sync.Mutex
	cache     map[string]vapidToken
}

type vapidToken struct {
	jwt     string
	refresh time.Time
}

func newVAPIDSigner(key *ecdsa.PrivateKey, subject string, now func() time.Time) (*vapidSigner, error) {
	public, err := key.PublicKey.ECDH()
	if err != nil {
		return nil, err
	}
	return &vapidSigner{key: key, subject: subject, now: now, cache: map[string]vapidToken{},
		publicKey: base64.RawURLEncoding.EncodeToString(public.Bytes())}, nil
}

// authorization returns the RFC 8292 header value for an endpoint's origin.
// Tokens last one hour, within every push service's limit, and are reused for 30 minutes.
func (s *vapidSigner) authorization(audience string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if cached, ok := s.cache[audience]; ok && now.Before(cached.refresh) {
		return "vapid t=" + cached.jwt + ", k=" + s.publicKey, nil
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, err := json.Marshal(map[string]any{"aud": audience, "exp": now.Add(time.Hour).Unix(), "sub": s.subject})
	if err != nil {
		return "", err
	}
	unsigned := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(unsigned))
	r, sig, err := ecdsa.Sign(rand.Reader, s.key, digest[:])
	if err != nil {
		return "", err
	}
	// JWS ES256 uses fixed-width r||s, not ASN.1.
	raw := make([]byte, 64)
	r.FillBytes(raw[:32])
	sig.FillBytes(raw[32:])
	jwt := unsigned + "." + base64.RawURLEncoding.EncodeToString(raw)
	s.cache[audience] = vapidToken{jwt: jwt, refresh: now.Add(30 * time.Minute)}
	return "vapid t=" + jwt + ", k=" + s.publicKey, nil
}

// Browser push services the server may POST to. Endpoints come from browsers,
// so anything else is refused to prevent server-side request forgery.
func allowedPushHost(host string) bool {
	switch host {
	case "fcm.googleapis.com", "updates.push.services.mozilla.com", "web.push.apple.com":
		return true
	}
	return strings.HasSuffix(host, ".notify.windows.com") && len(host) > len(".notify.windows.com")
}

// parsePushEndpoint validates a browser-supplied endpoint and returns it with
// its origin, the VAPID audience.
func parsePushEndpoint(raw string) (*url.URL, string, bool) {
	if len(raw) == 0 || len(raw) > 1024 {
		return nil, "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" || u.Fragment != "" || (u.Port() != "" && u.Port() != "443") {
		return nil, "", false
	}
	host := strings.ToLower(u.Hostname())
	if host != u.Hostname() || net.ParseIP(host) != nil || !allowedPushHost(host) {
		return nil, "", false
	}
	return u, "https://" + host, true
}

// decodePushKeys checks the browser's p256dh point and 16-byte auth secret.
func decodePushKeys(p256dh, auth string) ([]byte, []byte, bool) {
	point, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(p256dh, "="))
	if err != nil {
		return nil, nil, false
	}
	if _, err = ecdh.P256().NewPublicKey(point); err != nil {
		return nil, nil, false
	}
	secret, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(auth, "="))
	if err != nil || len(secret) != 16 {
		return nil, nil, false
	}
	return point, secret, true
}
