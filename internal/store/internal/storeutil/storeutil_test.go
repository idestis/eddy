package storeutil

import (
	"errors"
	"testing"
)

func TestCursorRoundTrip(t *testing.T) {
	tests := []struct {
		ms int64
		id string
	}{
		{0, "a"},
		{1790000000123, "01920000-0000-7000-8000-000000000000"},
		{-5, "id:with:colons"},
		{42, "123"},
	}
	for _, tc := range tests {
		c, ok, err := DecodeCursor(EncodeCursor(tc.ms, tc.id))
		if err != nil || !ok || c.Ms != tc.ms || c.ID != tc.id {
			t.Errorf("round trip (%d, %q) = %+v, %v, %v", tc.ms, tc.id, c, ok, err)
		}
	}
}

func TestDecodeCursorRejects(t *testing.T) {
	for _, s := range []string{"!!!", "bm9jb2xvbg", "Omlk", "eDppZA", "MTI6"} { // "nocolon", ":id", "x:id", "12:"
		if _, _, err := DecodeCursor(s); !errors.Is(err, ErrInvalidCursor) {
			t.Errorf("DecodeCursor(%q) err = %v, want ErrInvalidCursor", s, err)
		}
	}
	if _, ok, err := DecodeCursor(""); ok || err != nil {
		t.Errorf("empty cursor: ok=%v err=%v", ok, err)
	}
}

func TestClampLimit(t *testing.T) {
	tests := []struct{ in, want int }{{-1, 50}, {0, 50}, {1, 1}, {200, 200}, {201, 200}}
	for _, tc := range tests {
		if got := ClampLimit(tc.in, 50, 200); got != tc.want {
			t.Errorf("ClampLimit(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
