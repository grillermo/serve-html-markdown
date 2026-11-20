package web

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"serve-html-markdown/internal/config"
	"serve-html-markdown/internal/store"
)

// LAN trust lets the owner's own devices read pages without signing in, but only
// on a second listener (LAN_PORT) that the Cloudflare tunnel never reaches.
//
// The tunnel forwards serve.chiq.me to localhost:PORT, so every internet request
// arrives from 127.0.0.1: the source address alone cannot tell a LAN device from
// the internet. What can is the pair (port the connection was accepted on, its
// TCP source address). Headers such as X-Forwarded-For are never consulted: a
// client can set them to anything.
//
// Loopback addresses are refused even when listed, because a local proxy (the
// tunnel, WARP routing) would make internet traffic look like loopback.

// parseTrustedIPs parses LAN_TRUSTED_IPS (comma or space separated addresses).
// It fails on an invalid entry or a loopback address, so a typo stops startup
// instead of silently trusting the wrong host.
func parseTrustedIPs(raw []string) (map[netip.Addr]bool, error) {
	out := map[netip.Addr]bool{}
	for _, item := range raw {
		for _, f := range strings.FieldsFunc(item, func(r rune) bool { return r == ',' || r == ' ' }) {
			a, err := netip.ParseAddr(f)
			if err != nil {
				return nil, fmt.Errorf("LAN_TRUSTED_IPS: %q is not an IP address", f)
			}
			a = a.Unmap()
			if a.IsLoopback() || a.IsUnspecified() {
				return nil, fmt.Errorf("LAN_TRUSTED_IPS: %s cannot be trusted (loopback or unspecified)", a)
			}
			out[a] = true
		}
	}
	return out, nil
}

// lanTrusted reports whether r came in on the LAN listener from a trusted
// device. It reads the accepting address that net/http stores in the request
// context and the connection's RemoteAddr, both set by the server itself.
func (s *Server) lanTrusted(r *http.Request) bool {
	if s.cfg.LanPort == "" || len(s.lanTrustedIPs) == 0 {
		return false
	}
	local, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok {
		return false
	}
	_, port, err := net.SplitHostPort(local.String())
	if err != nil || port != s.cfg.LanPort {
		return false
	}
	remote, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	return s.lanTrustedIPs[remote.Addr().Unmap()]
}

// lanUser is the user a trusted LAN request acts as: the ADMIN_EMAIL user.
// It returns nil, nil when there is no such user, so the request falls back to
// the normal sign-in.
func (s *Server) lanUser(r *http.Request) (*store.User, error) {
	if s.cfg.AdminEmail == "" {
		return nil, nil
	}
	return store.FindUserByEmail(r.Context(), s.pool, s.cfg.AdminEmail)
}

// CheckLAN validates the LAN settings and returns the trusted addresses. With
// LAN_PORT unset it trusts nothing. With it set, it needs a valid, non-empty
// LAN_TRUSTED_IPS, an ADMIN_EMAIL to act as, and a port other than PORT.
func CheckLAN(cfg config.Config) (map[netip.Addr]bool, error) {
	if cfg.LanPort == "" {
		return nil, nil
	}
	if cfg.LanPort == cfg.Port {
		return nil, fmt.Errorf("LAN_PORT must differ from PORT (%s): the tunnel reaches PORT", cfg.Port)
	}
	if cfg.AdminEmail == "" {
		return nil, fmt.Errorf("LAN_PORT needs ADMIN_EMAIL, the user LAN requests act as")
	}
	trusted, err := parseTrustedIPs([]string{cfg.LanTrustedIPs})
	if err != nil {
		return nil, err
	}
	if len(trusted) == 0 {
		return nil, fmt.Errorf("LAN_PORT needs LAN_TRUSTED_IPS")
	}
	return trusted, nil
}
