package user

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"testing"
)

// User must not impose a socket-peer quota shared by gateway clients.
func TestProxyTrafficDoesNotShareUserIPQuota(t *testing.T) {
	a, _ := testApp(t)
	backend := httptest.NewServer(a.PublicHandler())
	defer backend.Close()
	target, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	director := proxy.Director
	proxy.Director = func(r *http.Request) {
		director(r)
		r.Header.Del("X-Forwarded-For")
	}
	throughProxy := func(client string, body any) int {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", bytes.NewReader(data))
		r.RemoteAddr = client
		r.Header.Set("Origin", a.cfg.Origin)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		proxy.ServeHTTP(w, r)
		return w.Code
	}
	// Invalid bodies exercise more than the former shared quota without password work.
	for i := range 65 {
		if got := throughProxy("203.0.113.10:10000", map[string]string{}); got != 400 {
			t.Fatalf("attacker request %d: got %d, want input rejection 400", i+1, got)
		}
	}
	got := throughProxy("203.0.113.20:20000", map[string]string{
		"email": "independent@u.nus.edu", "displayName": "Independent client", "password": testPassword,
	})
	if got != 202 {
		t.Fatalf("a second client was blocked by User after another proxy client sent 65 requests: got HTTP %d, want 202", got)
	}
}

// Account limits must apply even when clients change peer/forwarding addresses.
func TestLoginAccountLimitSurvivesDifferentClientAddresses(t *testing.T) {
 a,_:=testApp(t)
 for i:=0;i<11;i++ {
 data,_:=json.Marshal(map[string]string{"email":"same-account@u.nus.edu","password":testPassword})
 r:=httptest.NewRequest("POST","/api/v1/auth/login",bytes.NewReader(data))
 r.RemoteAddr=fmt.Sprintf("203.0.113.%d:1234",i+1)
 r.Header.Set("X-Forwarded-For",fmt.Sprintf("198.51.100.%d",i+1))
 r.Header.Set("Origin",a.cfg.Origin);r.Header.Set("Content-Type","application/json")
 w:=httptest.NewRecorder();a.PublicHandler().ServeHTTP(w,r)
 want:=401;if i==10 {want=429};if w.Code!=want {t.Fatalf("attempt %d: got %d want %d",i+1,w.Code,want)}
 }
}

func TestPasswordWorkCapIsRetained(t *testing.T) {
 a,_:=testApp(t)
 for range cap(a.passwordSlots) {a.passwordSlots<-struct{}{}}
 defer func(){for len(a.passwordSlots)>0 {<-a.passwordSlots}}()
 r:=httptest.NewRequest("POST","/api/v1/auth/login",bytes.NewBufferString("{}"));r.Header.Set("Origin",a.cfg.Origin);r.Header.Set("Content-Type","application/json")
 w:=httptest.NewRecorder();a.PublicHandler().ServeHTTP(w,r);status(t,w,503)
 if !bytes.Contains(w.Body.Bytes(),[]byte("busy")) {t.Fatal(w.Body.String())}
}
