package internetdata

import (
	"context"

	"github.com/internetdata/sdk-go/v2/internal/api"
)

// The account shapes, re-exported so a consumer never has to name an internal
// package.
type (
	// Entitlement is what a key is entitled to and what it has spent.
	Entitlement = api.Entitlement
	// EntitlementApikey is the credential itself. The key is never echoed back -
	// only its ID, which is what the console shows.
	EntitlementApikey = api.EntitlementApikey
	// EntitlementPlan is the plan behind the key, and the field tier that decides
	// how much of a lookup answer comes back.
	EntitlementPlan = api.EntitlementPlan
	// EntitlementUsage is consumption against the allowance.
	EntitlementUsage = api.EntitlementUsage
)

// MyEntitlement reports what this client's key is entitled to and how much of it
// has been used.
//
// Named for what it answers rather than `Me`, which sits one letter from MyIP
// and means something quite different: one is which address you are calling
// from, the other is what the key you are calling with may spend.
//
// Unlike a lookup there is no useful unauthenticated answer, so a client built
// without a key gets an unauthorized error rather than a partial one.
//
// Usage is counted against the ALLOWANCE WINDOW - on a paid plan the
// anniversary of the subscription, not the calendar month and not the
// billing period, and on the free plan the UTC day - and the number is the
// same one a lookup is gated on. It can lag by a few seconds, because
// requests are counted in memory and flushed in aggregate.
//
// Deliberately NOT cached: the whole point is what has been spent, and a
// cached answer is a wrong one within seconds of the next request.
func (c *Client) MyEntitlement(ctx context.Context, opts ...LookupOption) (*Entitlement, error) {
	call := c.callConfig()
	for _, opt := range opts {
		opt.applyLookup(&call)
	}
	if err := call.refused(); err != nil {
		return nil, err
	}
	ctx = call.carry(ctx)
	return withRetry(ctx, call.retries, func() (*Entitlement, error) {
		return decodeAnswer[Entitlement](c.api.MyEntitlement(ctx))
	})
}
