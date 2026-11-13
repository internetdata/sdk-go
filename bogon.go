package internetdata

import (
	"fmt"
	"net/netip"
	"reflect"
	"sync"
)

// IsBogon reports whether an address is private, loopback, link-local,
// documentation, multicast or otherwise not routable on the public internet,
// including the IPv6 equivalents and the 6to4 and Teredo ranges that wrap them.
//
// The API answers each with every flag false and every key empty, so the
// client answers them itself and they never cost a request. Client.IsBogon is
// the same check, for code that already holds a client. An IPv4-mapped address
// (::ffff:8.8.8.8) is judged as the IPv4 address it carries.
func IsBogon(ip string) bool {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	prefixes := bogonPrefixesV6
	if addr.Is4() {
		prefixes = bogonPrefixesV4
	}
	for _, prefix := range prefixes() {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// unmapped is the IPv4 address an IPv4-mapped IPv6 address carries, dotted, and
// any other string as given. A server listening on :: can see an IPv4 visitor
// in the mapped form, which read whole is inside ::ffff:0:0/96 and so would be
// answered as a bogon with no request made.
func unmapped(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil || !addr.Is4In6() {
		return ip
	}
	return addr.Unmap().String()
}

// The answer a bogon gets, the one the API gives a bogon on a paid plan: every
// flag present and false, every detail object present and empty (web an empty
// list), location, asn, company, abuse, whois and rwhois with every key empty,
// hostname empty, hosted_domains 0 and is_bogon true.
//
// Deliberately the WIDEST shape whatever plan you are on, so a caller must not
// infer which fields their plan includes from a bogon answer.
func bogonResult(ip string) *Result {
	return &Result{
		LookupResponse: LookupResponse{
			IP:            ip,
			IsBogon:       true,
			IsVpn:         ptr(false),
			IsResproxy:    ptr(false),
			IsDcproxy:     ptr(false),
			IsMobproxy:    ptr(false),
			IsPubproxy:    ptr(false),
			IsHosting:     ptr(false),
			IsCdn:         ptr(false),
			Vpn:           &ClassDetail{},
			Resproxy:      &ProxyDetail{},
			Dcproxy:       &ProxyDetail{},
			Mobproxy:      &ProxyDetail{},
			Pubproxy:      &PubproxyDetail{},
			Hosting:       &ClassDetail{},
			Cdn:           &ClassDetail{},
			Carrier:       &CarrierDetail{},
			Satellite:     &SatelliteDetail{},
			SSH:           &SSHDetail{},
			SMTP:          &SMTPDetail{},
			Web:           &[]WebDetail{},
			DNSResolver:   &DNSResolverDetail{},
			Bittorrent:    &BitTorrentDetail{},
			Location:      *everyKey[Location](),
			Asn:           *everyKey[Asn](),
			Company:       *everyKey[Company](),
			Abuse:         *everyKey[AbuseContact](),
			Whois:         everyKey[Whois](),
			Rwhois:        everyKey[Rwhois](),
			HostedDomains: &HostedDomains{},
		},
	}
}

// everyKey is a T with every member present and empty: each pointer member,
// one a paid plan answers, points at its zero value.
func everyKey[T any]() *T {
	var v T
	value := reflect.ValueOf(&v).Elem()
	for i := range value.NumField() {
		if field := value.Field(i); field.Kind() == reflect.Pointer {
			field.Set(reflect.New(field.Type().Elem()))
		}
	}
	return &v
}

// Parsed on first use rather than at init, so importing the package costs
// nothing for a consumer that never looks an address up.
var (
	bogonPrefixesV4 = sync.OnceValue(func() []netip.Prefix { return parsePrefixes(bogonV4) })
	bogonPrefixesV6 = sync.OnceValue(func() []netip.Prefix { return parsePrefixes(bogonV6) })
)

// The table is generated and checked upstream, and TestBogonTableParses asserts
// every entry here, so an unparseable one is a broken build rather than
// something a caller can hit.
func parsePrefixes(cidrs []string) []netip.Prefix {
	prefixes := make([]netip.Prefix, 0, len(cidrs))
	for _, cidr := range cidrs {
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil {
			panic(fmt.Sprintf("internetdata: unparseable bogon range %q: %v", cidr, err))
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes
}

func ptr[T any](v T) *T {
	return &v
}
