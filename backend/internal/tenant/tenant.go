// Package tenant carries the active band through the request context.
//
// The band filter is never written by hand in a query. It is applied by a GORM
// callback (see internal/db/tenant_callback.go) that refuses to run any
// statement touching a band-scoped table without a scope in the context. A
// forgotten filter therefore becomes a loud error instead of a data leak.
package tenant

import (
	"context"
	"errors"
	"fmt"
)

// Errors returned by the scope helpers and by the GORM callback.
var (
	// ErrMissingScope means a band-scoped table was touched without any scope
	// in the context. This is a programming error, not a user error.
	ErrMissingScope = errors.New("tenant: no band scope in context")
	// ErrScopeMismatch means a record was written with a band_id other than
	// the scoped band.
	ErrScopeMismatch = errors.New("tenant: record belongs to a different band")
	// ErrReadOnlyScope means a write was attempted under a read-only support
	// access grant.
	ErrReadOnlyScope = errors.New("tenant: scope is read-only")
)

type contextKey struct{}

// Scope describes which band the current request may touch and how.
type Scope struct {
	// BandID is the band being operated on. Zero only when CrossBand is set.
	BandID int64
	// CrossBand allows a statement to run without a band filter. It is set
	// only by explicit platform-level code paths such as the admin center's
	// band list, the audit viewer and the backup scheduler.
	CrossBand bool
	// GrantID is set when the scope came from a support access grant, so the
	// audit log can record under which approval an action ran.
	GrantID *int64
	// ReadOnly rejects every write, which is what a read_only grant means.
	ReadOnly bool
}

// WithBand scopes every subsequent query to one band.
func WithBand(ctx context.Context, bandID int64) context.Context {
	return context.WithValue(ctx, contextKey{}, Scope{BandID: bandID})
}

// WithGrant scopes to one band under a support access grant. Writes are
// rejected unless the grant allows them.
func WithGrant(ctx context.Context, bandID int64, grantID int64, readOnly bool) context.Context {
	return context.WithValue(ctx, contextKey{}, Scope{
		BandID:   bandID,
		GrantID:  &grantID,
		ReadOnly: readOnly,
	})
}

// WithCrossBandAccess lifts the band filter for genuine platform work.
//
// Use it only where operating across all bands is the point — the band list,
// the cross-band audit viewer, the backup scheduler, the migration bootstrap.
// Never use it to work around a missing scope in a band request path.
func WithCrossBandAccess(ctx context.Context) context.Context {
	return context.WithValue(ctx, contextKey{}, Scope{CrossBand: true})
}

// FromContext returns the active scope.
func FromContext(ctx context.Context) (Scope, bool) {
	scope, ok := ctx.Value(contextKey{}).(Scope)
	return scope, ok
}

// BandID returns the scoped band, or an error when no band is scoped.
func BandID(ctx context.Context) (int64, error) {
	scope, ok := FromContext(ctx)
	if !ok || scope.CrossBand || scope.BandID == 0 {
		return 0, ErrMissingScope
	}
	return scope.BandID, nil
}

// MustBandID is for code paths where the middleware guarantees a band scope.
func MustBandID(ctx context.Context) int64 {
	id, err := BandID(ctx)
	if err != nil {
		panic(fmt.Sprintf("tenant: %v", err))
	}
	return id
}

// GrantID returns the support-access grant the request runs under, if any.
func GrantID(ctx context.Context) *int64 {
	scope, ok := FromContext(ctx)
	if !ok {
		return nil
	}
	return scope.GrantID
}

// IsReadOnly reports whether writes are forbidden in this scope.
func IsReadOnly(ctx context.Context) bool {
	scope, ok := FromContext(ctx)
	return ok && scope.ReadOnly
}
