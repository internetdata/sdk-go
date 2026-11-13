package internetdata

import (
	"github.com/internetdata/sdk-go/v2/internal/api"
)

// The wire shapes a lookup answers with, re-exported so a consumer never has to
// name an internal package.
type (
	LookupResponse    = api.LookupResponse
	ClassDetail       = api.ClassDetail
	ProxyDetail       = api.ProxyDetail
	PubproxyDetail    = api.PubproxyDetail
	CarrierDetail     = api.CarrierDetail
	SatelliteDetail   = api.SatelliteDetail
	SSHDetail         = api.SSHDetail
	SMTPDetail        = api.SMTPDetail
	WebDetail         = api.WebDetail
	DNSResolverDetail = api.DNSResolverDetail
	BitTorrentDetail  = api.BitTorrentDetail
	// Location is where the address is, on every answer, with or without a
	// key. The members beyond the country and its currency are a paid plan's.
	Location = api.Location
	// Asn is the AS announcing the address, on every answer, with or without a
	// key: Asn is 0 where none does.
	Asn              = api.Asn
	Company          = api.Company
	AbuseContact     = api.AbuseContact
	Whois            = api.Whois
	Rwhois           = api.Rwhois
	HostedDomains    = api.HostedDomains
	AsnPrefix        = api.AsnPrefix
	DomainDNS        = api.DomainDNS
	DNSAddressRecord = api.DNSAddressRecord
	DNSMxRecord      = api.DNSMxRecord
	DNSNsRecord      = api.DNSNsRecord
	DNSCnameRecord   = api.DNSCnameRecord
	DomainIPRanges   = api.DomainIPRanges
	DomainIPRange    = api.DomainIPRange
	// AsnLookupResponse is what LookupASN answers.
	AsnLookupResponse = api.AsnLookupResponse
	// DomainLookupResponse is what LookupDomain answers.
	DomainLookupResponse = api.DomainLookupResponse
	DisposableDetail     = api.DisposableDetail
)

// Result is what a lookup answers.
//
// A pointer member is one your plan does not include. It never means "we could
// not check", so nil and false are genuinely different answers: nil is "not in
// your plan", false is "checked, and no". BoolValue collapses the two for
// callers who only want to know whether an address is flagged. The same holds
// inside a shared object, so Location.City is nil on Unlimited.
//
// A detail object that is present but empty means the flag above it is false. A
// populated one always carries every one of its keys, empty values included.
// IsBogon is true for a private or reserved address, which the client answers
// itself.
type Result struct {
	// The answer as it was served, with the wire's own optionality intact. Its
	// fields are promoted, so r.IsTor and r.Location read straight off the
	// Result.
	LookupResponse
}

// BoolValue returns the value of the bool pointer passed in, or false if the
// pointer is nil.
//
// Go has no `??`, so this is how a caller who only wants to know whether an
// address is flagged reads a tier-gated member: BoolValue(r.IsVpn). Read the
// pointer itself wherever absent and false must be told apart, which is the
// whole reason these are pointers. Same name and behavior as stripe-go's
// helper of the same name, so it should already be familiar.
func BoolValue(v *bool) bool {
	if v != nil {
		return *v
	}
	return false
}
