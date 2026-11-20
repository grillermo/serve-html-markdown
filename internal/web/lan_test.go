package web

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"serve-html-markdown/internal/auth"
	"serve-html-markdown/internal/config"
	"serve-html-markdown/internal/db/testdb"
	"serve-html-markdown/internal/store"
)

const (
	testMainPort = 8009
	testLanPort  = 8010
	trustedIP    = "192.168.1.2"
)

// newLANEnv is an authEnv whose server has the LAN listener configured, trusting
// trustedIP and acting as viewer@example.com.
func newLANEnv(t *testing.T) *authEnv {
	t.Helper()
	cfg := testConfig(t)
	cfg.Port, cfg.LanPort = "8009", "8010"
	cfg.LanTrustedIPs = trustedIP + ", 192.168.1.4"
	cfg.AdminEmail = viewerEmail
	env := &authEnv{now: new(time.Time)}
	*env.now = fixedNow
	env.srv = New(cfg, testdb.New(t), WithClock(func() time.Time { return *env.now }))
	env.user = env.addUser(t, viewerEmail, viewerPassword)
	return env
}

// lanRequest is a request as net/http hands it over: accepted on localPort,
// from remoteIP.
func lanRequest(method, path, remoteIP string, localPort int) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = net.JoinHostPort(remoteIP, "51000")
	local := &net.TCPAddr{IP: net.ParseIP("192.168.1.1"), Port: localPort}
	return req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, local))
}

func serve(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestLANTrustedDeviceReadsWithoutSigningIn(t *testing.T) {
	env := newLANEnv(t)
	if rec := serve(env.srv, lanRequest("GET", "/index", trustedIP, testLanPort)); rec.Code != 200 {
		t.Fatalf("trusted device on the LAN port: got %d, want 200", rec.Code)
	}
	// IPv4-mapped IPv6 is the same device.
	if rec := serve(env.srv, lanRequest("GET", "/index", "::ffff:"+trustedIP, testLanPort)); rec.Code != 200 {
		t.Fatalf("mapped address: got %d, want 200", rec.Code)
	}
}

func TestLANTrustActsAsAdminUser(t *testing.T) {
	env := newLANEnv(t)
	var got *store.User
	h := env.srv.requireUser(func(w http.ResponseWriter, r *http.Request) {
		got = auth.UserFrom(r.Context())
		w.WriteHeader(204)
	})
	if rec := serve(h, lanRequest("GET", "/index", trustedIP, testLanPort)); rec.Code != 204 || got == nil || got.ID != env.user.ID {
		t.Fatalf("code %d user %+v", rec.Code, got)
	}
}

func TestLANTrustIsRefused(t *testing.T) {
	env := newLANEnv(t)
	cases := []struct {
		name string
		req  *http.Request
	}{
		// The tunnel's port: the internet must sign in even from a listed address.
		{"trusted IP on the main port", lanRequest("GET", "/index", trustedIP, testMainPort)},
		{"unlisted IP on the LAN port", lanRequest("GET", "/index", "192.168.1.77", testLanPort)},
		{"loopback on the LAN port", lanRequest("GET", "/index", "127.0.0.1", testLanPort)},
		{"no local address in the context", func() *http.Request {
			r := httptest.NewRequest("GET", "/index", nil)
			r.RemoteAddr = trustedIP + ":51000"
			return r
		}()},
		{"spoofed forwarding headers", func() *http.Request {
			r := lanRequest("GET", "/index", "127.0.0.1", testLanPort)
			r.Header.Set("X-Forwarded-For", trustedIP)
			r.Header.Set("Cf-Connecting-Ip", trustedIP)
			r.Header.Set("X-Real-Ip", trustedIP)
			return r
		}()},
		// Account-level routes need a real session even on the LAN.
		{"YouTube reauth", lanRequest("GET", "/youtube/reauth", trustedIP, testLanPort)},
	}
	for _, c := range cases {
		rec := serve(env.srv, c.req)
		if rec.Code != 302 || !strings.HasPrefix(rec.Header().Get("Location"), "/users/sign_in") {
			t.Errorf("%s: got %d to %q, want a redirect to sign in", c.name, rec.Code, rec.Header().Get("Location"))
		}
	}
}

func TestLANTrustOffWithoutLanPort(t *testing.T) {
	env := newAuthEnv(t) // no LAN_PORT
	if rec := serve(env.srv, lanRequest("GET", "/index", trustedIP, testLanPort)); rec.Code != 302 {
		t.Fatalf("got %d, want 302", rec.Code)
	}
}

func TestCheckLAN(t *testing.T) {
	ok := config.Config{Port: "8009", LanPort: "8010", AdminEmail: "a@b.c", LanTrustedIPs: "192.168.1.2,192.168.1.4 192.168.1.6"}
	trusted, err := CheckLAN(ok)
	if err != nil || len(trusted) != 3 {
		t.Fatalf("valid config: %v, %d trusted", err, len(trusted))
	}
	if trusted, err := CheckLAN(config.Config{Port: "8009"}); err != nil || trusted != nil {
		t.Fatalf("LAN off: %v %v", trusted, err)
	}
	bad := map[string]func(c *config.Config){
		"same port":    func(c *config.Config) { c.LanPort = c.Port },
		"no admin":     func(c *config.Config) { c.AdminEmail = "" },
		"no IPs":       func(c *config.Config) { c.LanTrustedIPs = " , " },
		"loopback":     func(c *config.Config) { c.LanTrustedIPs = "192.168.1.2,127.0.0.1" },
		"ipv6 lo":      func(c *config.Config) { c.LanTrustedIPs = "::1" },
		"unspecified":  func(c *config.Config) { c.LanTrustedIPs = "0.0.0.0" },
		"not an IP":    func(c *config.Config) { c.LanTrustedIPs = "192.168.1.0/24" },
		"hostname too": func(c *config.Config) { c.LanTrustedIPs = "macmini" },
	}
	for name, mutate := range bad {
		c := ok
		mutate(&c)
		if _, err := CheckLAN(c); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
