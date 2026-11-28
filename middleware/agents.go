package middleware

import (
	"crypto/sha256"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
)

// MaxAgentsPerAddress is the most crawler agents one address is looked up with
// while the middleware remembers it, an hour from its first.
const MaxAgentsPerAddress = 4

const (
	// agentsTTL is the client's default cache TTL.
	agentsTTL = time.Hour
	// agentsMax is how many addresses are remembered at once.
	agentsMax = 10_000
)

// NamesCrawler reports whether a User-Agent names a crawler, the only kind of
// agent whose verdict can be verified or spoofed: its ASCII-lowercased form
// contains one of the crawler terms. It errs toward a match, which costs a
// lookup.
func NamesCrawler(agent string) bool {
	bytes := []byte(agent)
	for i, c := range bytes {
		if 'A' <= c && c <= 'Z' {
			bytes[i] = c + 'a' - 'A'
		}
	}
	lower := string(bytes)
	for _, term := range crawlerTerms {
		if strings.Contains(lower, term) {
			return true
		}
	}
	return false
}

// agentClaim is what one request's agent comes to under AllowBots.
type agentClaim struct {
	// named is whether the agent names a crawler: only such a request can
	// pass as one.
	named bool
	// sent is the agent the lookup carries, empty past the address's bound.
	sent string
}

// agentLedger remembers the crawler agents each address was looked up with, so
// a visitor inventing an agent per request spends at most MaxAgentsPerAddress
// lookups an hour. An agent is held as its digest, which bounds what an
// address costs whatever the header's length.
type agentLedger struct {
	mu   sync.Mutex
	seen *expirable.LRU[string, *addressAgents]
}

type addressAgents struct {
	digests [][sha256.Size]byte
}

func newAgentLedger() *agentLedger {
	return &agentLedger{seen: expirable.NewLRU[string, *addressAgents](agentsMax, nil, agentsTTL)}
}

// claim records agent against ip when it names a crawler and the address has
// room, and says whether the lookup sends it. A known agent is sent again.
func (l *agentLedger) claim(ip, agent string) agentClaim {
	if agent == "" || !NamesCrawler(agent) {
		return agentClaim{}
	}
	digest := sha256.Sum256([]byte(agent))
	l.mu.Lock()
	defer l.mu.Unlock()
	known, ok := l.seen.Get(ip)
	if !ok {
		known = &addressAgents{}
		l.seen.Add(ip, known)
	}
	if slices.Contains(known.digests, digest) {
		return agentClaim{named: true, sent: agent}
	}
	if len(known.digests) >= MaxAgentsPerAddress {
		return agentClaim{named: true}
	}
	// Appended in place: Add again would restart the address's hour.
	known.digests = append(known.digests, digest)
	return agentClaim{named: true, sent: agent}
}
