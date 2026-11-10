package order

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"
)

func b64url(t *testing.T, s string) []byte {
	t.Helper()
	data, err := base64.RawURLEncoding.DecodeString(strings.Join(strings.Fields(s), ""))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// RFC 8291 Appendix A: inputs and the expected 86-octet header plus ciphertext.
func TestEncryptPushMatchesRFC8291Vector(t *testing.T) {
	asPrivate, err := ecdh.P256().NewPrivateKey(b64url(t, "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"))
	if err != nil {
		t.Fatal(err)
	}
	uaPublic := b64url(t, "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcx aOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4")
	got, err := encryptPush([]byte("When I grow up, I want to be a watermelon"), uaPublic,
		b64url(t, "BTBZMqHH6r4Tts7J_aSIgg"), asPrivate, b64url(t, "DGv6ra1nlYgDCS1FRnbzlw"))
	if err != nil {
		t.Fatal(err)
	}
	want := append(b64url(t, `DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z 9KsN6nGRTbVYI_c7VJSPQTBtkgcy27ml
		mlMoZIIgDll6e3vCYLocInmYWAmS6Tlz AC8wEqKK6PBru3jl7A8`),
		b64url(t, "8pfeW0KbunFT06SuDKoJH9Ql87S1QUrd irN6GcG7sFz1y1sqLgVi1VhjVkHsUoEs bI_0LpXMuGvnzQ")...)
	if !bytes.Equal(got, want) {
		t.Fatalf("RFC 8291 vector mismatch:\n got %x\nwant %x", got, want)
	}
}

// decryptPush is the browser's side of RFC 8291, used to check fresh messages.
func decryptPush(t *testing.T, body []byte, ua *ecdh.PrivateKey, auth []byte) []byte {
	t.Helper()
	if len(body) < 86 || binary.BigEndian.Uint32(body[16:20]) != pushRecordSize || body[20] != 65 {
		t.Fatal("malformed aes128gcm header")
	}
	salt, asPublic := body[:16], body[21:86]
	sender, err := ecdh.P256().NewPublicKey(asPublic)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := ua.ECDH(sender)
	if err != nil {
		t.Fatal(err)
	}
	ikm, _ := hkdf.Key(sha256.New, secret, auth, "WebPush: info\x00"+string(ua.PublicKey().Bytes())+string(asPublic), 32)
	cek, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, body[86:], nil)
	if err != nil || len(plain) == 0 || plain[len(plain)-1] != 0x02 {
		t.Fatal("push message does not decrypt", err)
	}
	return plain[:len(plain)-1]
}

func TestSealPushUsesFreshKeysAndDecrypts(t *testing.T) {
	ua, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	first, err := sealPush([]byte(`{"v":1}`), ua.PublicKey().Bytes(), auth)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := sealPush([]byte(`{"v":1}`), ua.PublicKey().Bytes(), auth)
	if bytes.Equal(first[:86], second[:86]) {
		t.Fatal("salt and ephemeral key were reused")
	}
	if string(decryptPush(t, first, ua, auth)) != `{"v":1}` {
		t.Fatal("round trip failed")
	}
	if _, err = sealPush(bytes.Repeat([]byte("x"), pushRecordSize), ua.PublicKey().Bytes(), auth); err == nil {
		t.Fatal("oversized payload accepted")
	}
	if _, err = sealPush([]byte("x"), make([]byte, 65), auth); err == nil {
		t.Fatal("invalid browser key accepted")
	}
}

func TestVAPIDAuthorizationVerifiesAndIsCached(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	now := time.Unix(1_800_000_000, 0)
	signer, err := newVAPIDSigner(key, "https://github.com/AY2627S1-CS3219-P13/FoC-Template", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	header, err := signer.authorization("https://fcm.googleapis.com")
	if err != nil {
		t.Fatal(err)
	}
	var jwt, k string
	for _, part := range strings.Split(strings.TrimPrefix(header, "vapid "), ", ") {
		if v, ok := strings.CutPrefix(part, "t="); ok {
			jwt = v
		} else if v, ok := strings.CutPrefix(part, "k="); ok {
			k = v
		}
	}
	public, _ := key.PublicKey.ECDH()
	if !bytes.Equal(b64url(t, k), public.Bytes()) {
		t.Fatal("k is not the uncompressed public key")
	}
	segments := strings.Split(jwt, ".")
	if len(segments) != 3 {
		t.Fatal("not a JWS compact token")
	}
	var head map[string]string
	var claims struct {
		Aud, Sub string
		Exp      int64
	}
	if json.Unmarshal(b64url(t, segments[0]), &head) != nil || head["alg"] != "ES256" || head["typ"] != "JWT" {
		t.Fatal("bad JWT header", head)
	}
	if json.Unmarshal(b64url(t, segments[1]), &claims) != nil || claims.Aud != "https://fcm.googleapis.com" ||
		claims.Sub != "https://github.com/AY2627S1-CS3219-P13/FoC-Template" || claims.Exp != now.Add(time.Hour).Unix() {
		t.Fatalf("bad claims %+v", claims)
	}
	signature := b64url(t, segments[2])
	digest := sha256.Sum256([]byte(segments[0] + "." + segments[1]))
	if len(signature) != 64 || !ecdsa.Verify(&key.PublicKey, digest[:], new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])) {
		t.Fatal("ES256 signature does not verify")
	}
	if again, _ := signer.authorization("https://fcm.googleapis.com"); again != header {
		t.Fatal("token not reused within 30 minutes")
	}
	if other, _ := signer.authorization("https://web.push.apple.com"); other == header {
		t.Fatal("token shared across push services")
	}
	now = now.Add(31 * time.Minute)
	if renewed, _ := signer.authorization("https://fcm.googleapis.com"); renewed == header {
		t.Fatal("token not renewed")
	}
}

func TestPushEndpointAllowlist(t *testing.T) {
	for raw, audience := range map[string]string{
		"https://fcm.googleapis.com/fcm/send/abc:def":             "https://fcm.googleapis.com",
		"https://updates.push.services.mozilla.com/wpush/v2/gAAA": "https://updates.push.services.mozilla.com",
		"https://wns2-sg2p.notify.windows.com/w/?token=AwYA":      "https://wns2-sg2p.notify.windows.com",
		"https://web.push.apple.com/QGuQyavXutnMH":                "https://web.push.apple.com",
		"https://fcm.googleapis.com:443/fcm/send/abc":             "https://fcm.googleapis.com",
	} {
		if _, got, ok := parsePushEndpoint(raw); !ok || got != audience {
			t.Fatalf("%s rejected or wrong audience %q", raw, got)
		}
	}
	for _, raw := range []string{
		"", "http://fcm.googleapis.com/fcm/send/abc", "https://fcm.googleapis.com:8443/x",
		"https://user@fcm.googleapis.com/x", "https://127.0.0.1/x", "https://[::1]/x",
		"https://example.com/x", "https://notify.windows.com/x", "https://.notify.windows.com/x",
		"https://fcm.googleapis.com.example.com/x", "https://FCM.googleapis.com/x",
		"https://fcm.googleapis.com/x#fragment", "https://fcm.googleapis.com/" + strings.Repeat("a", 1024),
	} {
		if _, _, ok := parsePushEndpoint(raw); ok {
			t.Fatalf("%q accepted", raw)
		}
	}
}

func TestDecodePushKeys(t *testing.T) {
	ua, _ := ecdh.P256().GenerateKey(rand.Reader)
	point := base64.RawURLEncoding.EncodeToString(ua.PublicKey().Bytes())
	auth := base64.RawURLEncoding.EncodeToString(make([]byte, 16))
	if _, _, ok := decodePushKeys(point, auth); !ok {
		t.Fatal("valid keys rejected")
	}
	if _, _, ok := decodePushKeys(point+"==", auth+"=="); !ok {
		t.Fatal("padded base64url rejected")
	}
	offCurve := make([]byte, 65)
	offCurve[0] = 4
	for _, keys := range [][2]string{
		{base64.RawURLEncoding.EncodeToString(offCurve), auth},
		{point, base64.RawURLEncoding.EncodeToString(make([]byte, 15))},
		{"not base64!", auth},
	} {
		if _, _, ok := decodePushKeys(keys[0], keys[1]); ok {
			t.Fatal("invalid keys accepted", keys)
		}
	}
}

func encodedPKCS8(t *testing.T, key any) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(der)
}

func TestLoadVAPIDConfiguration(t *testing.T) {
	p256, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	valid := encodedPKCS8(t, p256)
	if key, err := loadVAPID("", ""); key != nil || err != nil {
		t.Fatal("alerts must be optional")
	}
	for _, subject := range []string{"https://github.com/AY2627S1-CS3219-P13/FoC-Template", "mailto:team@example.org"} {
		if key, err := loadVAPID(valid, subject); err != nil || key == nil {
			t.Fatal("valid VAPID configuration rejected", subject, err)
		}
	}
	for _, c := range [][2]string{
		{valid, ""}, {"", "mailto:team@example.org"}, {"not-base64", "mailto:team@example.org"},
		{encodedPKCS8(t, p384), "mailto:team@example.org"}, {encodedPKCS8(t, rsaKey), "mailto:team@example.org"},
		{valid, "mailto:admin@localhost"}, {valid, "http://example.org"}, {valid, "team@example.org"},
		{valid, "mailto:team@example.org extra"}, {valid, "https://user@example.org"},
	} {
		if _, err := loadVAPID(c[0], c[1]); err == nil {
			t.Fatalf("invalid VAPID configuration accepted: %q", c[1])
		}
	}

	t.Setenv("ORDER_DATABASE_URL", "postgres://test:test@localhost/test")
	t.Setenv("ORDER_APP_ORIGIN", "http://localhost:3000")
	t.Setenv("ORDER_USER_SERVICE_URL", "http://user:8081")
	t.Setenv("ORDER_USER_INTERNAL_TOKEN", "uuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuu")
	t.Setenv("ORDER_SUPPLIER_SERVICE_URL", "http://supplier:8080")
	t.Setenv("ORDER_CREDIT_SERVICE_URL", "http://credit:8081")
	t.Setenv("ORDER_CREDIT_INTERNAL_TOKEN", "cccccccccccccccccccccccccccccccc")
	t.Setenv("ORDER_VAPID_PRIVATE_KEY", valid)
	t.Setenv("ORDER_VAPID_SUBJECT", "mailto:team@example.org")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("alerts started without the Supplier internal catalogue")
	}
	t.Setenv("ORDER_SUPPLIER_INTERNAL_URL", "http://supplier:8080")
	t.Setenv("ORDER_SUPPLIER_INTERNAL_TOKEN", "ssssssssssssssssssssssssssssssss")
	cfg, err := LoadConfig()
	if err != nil || cfg.VAPIDKey == nil || cfg.VAPIDSubject != "mailto:team@example.org" {
		t.Fatal("valid alerts configuration rejected", err)
	}
	t.Setenv("ORDER_VAPID_SUBJECT", "")
	if _, err = LoadConfig(); err == nil {
		t.Fatal("key without subject accepted")
	}
}

func TestAlertPayloadNamesPlaceAndDistanceOnly(t *testing.T) {
	p := alertPayload("order/1", Place{Type: "supplier", ID: testSupplierID, Name: "Campus Cafe", LocationName: "COM2"}, 315)
	if p["body"] != "Campus Cafe (COM2) · about 320 m from your alert location. Open Help a friend to see all nearby errands." ||
		p["url"] != "/orders?view=open&order=order%2F1" || p["title"] != "New errand nearby" || p["orderId"] != "order/1" {
		t.Fatalf("unexpected payload %+v", p)
	}
	for meters, want := range map[int64]string{0: "under 50 m", 49: "under 50 m", 50: "about 50 m", 829: "about 830 m", 1123: "about 1.1 km"} {
		if got := alertDistance(meters); got != want {
			t.Fatalf("%d m: got %q want %q", meters, got, want)
		}
	}
	if same := alertPayload("x", Place{Type: "supplier", ID: testSupplierID, Name: "Campus Cafe", LocationName: "COM2"}, 0); same["body"] != "Campus Cafe (COM2) · at your alert location. Open Help a friend to see all nearby errands." {
		t.Fatalf("same-location payload %+v", same)
	}
	if location := alertPayload("x", Place{Type: "location", ID: "com2", Name: "COM2"}, 100); !strings.HasPrefix(location["body"].(string), "COM2 · about 100 m") {
		t.Fatal("location pickup body", location["body"])
	}
}
