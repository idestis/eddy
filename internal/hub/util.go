package hub

import (
	"context"
	"time"
)

type userSlotKey struct{}

// withUserSlot stores a pointer that recordUser fills with the
// authenticated user, so the outer access log can name the caller.
func withUserSlot(ctx context.Context, slot *string) context.Context {
	return context.WithValue(ctx, userSlotKey{}, slot)
}

func userSlot(ctx context.Context) *string {
	s, _ := ctx.Value(userSlotKey{}).(*string)
	return s
}

func timeFromNanos(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n).UTC()
}
