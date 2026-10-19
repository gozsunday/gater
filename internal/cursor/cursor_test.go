package cursor_test

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gozsunday/gater/internal/cursor"
)

func newTestCursor(ts time.Time) cursor.Cursor {
	return cursor.Cursor{
		ID:        uuid.MustParse("1da768f6-75b1-47f2-95bf-ba6008c5e209"),
		Timestamp: ts,
	}
}

func assertRoundtrip(t *testing.T, want cursor.Cursor) {
	t.Helper()

	decoded, err := cursor.Decode(cursor.Encode(want))
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	if want.ID != decoded.ID {
		t.Errorf("decoded cursor ID = %s, want %s", decoded.ID, want.ID)
	}
	if !want.Timestamp.Equal(decoded.Timestamp) {
		t.Errorf(
			"decoded cursor timestamp = %s, want %s",
			decoded.Timestamp, want.Timestamp,
		)
	}
}

func TestCursorRoundtrip(t *testing.T) {
	// 123456789ns has no trailing zeros, so RFC3339Nano trimming is not in play here
	want := newTestCursor(time.Date(2020, time.January, 7, 12, 0, 0, 123456789, time.FixedZone("WAT", 1*60*60)))
	assertRoundtrip(t, want)
}

func TestCursorRoundtripPreservesNanoseconds(t *testing.T) {
	// nanoseconds ending in zeros: RFC3339Nano trims them when encoding so
	// (100000000ns becomes ".1"), and the decode must still land on the same instant
	want := newTestCursor(time.Date(2020, time.January, 7, 12, 0, 0, 100000000, time.FixedZone("WAT", 1*60*60)))

	assertRoundtrip(t, want)
}

func TestCursorRoundtripZeroValues(t *testing.T) {
	want := cursor.Cursor{
		ID:        uuid.Nil,
		Timestamp: time.Time{},
	}
	assertRoundtrip(t, want)
}

// encodeRaw builds the base64url form that cursor.Decode expects
func encodeRaw(raw string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func TestCursorDecodeRejectsMalformedInput(t *testing.T) {
	validID := uuid.MustParse("0b4fbd1e-608c-4adf-96d9-28d518766d2b")

	tests := []struct {
		name    string
		encoded string
	}{
		{name: "not base64", encoded: "!!!not-base64!!!"},
		{name: "empty string", encoded: ""},
		{name: "missing comma", encoded: encodeRaw("no-comma-separator")},
		{name: "invalid timestamp", encoded: encodeRaw("not-a-time," + validID.String())},
		{name: "invalid uuid", encoded: encodeRaw("2026-01-01T00:00:00Z,not-a-uuid")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			decoded, err := cursor.Decode(tt.encoded)
			if err == nil {
				t.Fatalf("decode %s: expected an error, got nil", tt.encoded)
			}
			if decoded != (cursor.Cursor{}) {
				t.Errorf("decode %s: got cursor %+v, want zero value", tt.encoded, decoded)
			}
		})
	}
}
