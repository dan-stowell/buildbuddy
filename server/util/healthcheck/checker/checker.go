package checker

import "context"

type Checker interface {
	// Returns nil on success, error on failure. Returning an error will
	// indicate to the health checker that this service is unhealthy and
	// that the server is not ready to serve.
	Check(ctx context.Context) error
}
type CheckerFunc func(ctx context.Context) error

func (f CheckerFunc) Check(ctx context.Context) error {
	return f(ctx)
}
