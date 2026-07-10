package api

import "context"

// contextWithUser returns a copy of ctx carrying the authenticated username.
func contextWithUser(ctx context.Context, username string) context.Context {
	return context.WithValue(ctx, userKey{}, username)
}

// userFromContext returns the username stashed by the auth middleware. It is
// only called on authenticated routes, so the value is always present.
func userFromContext(ctx context.Context) string {
	user, _ := ctx.Value(userKey{}).(string)
	return user
}
