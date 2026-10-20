package qr_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/gozsunday/gater/internal/qr"
)

const testSecret = "test_secret"

var (
	ticketID   = uuid.MustParse("40e6b775-a821-4d36-8fec-cbb8e1de51cf")
	purchaseID = uuid.MustParse("8d6e0137-ebd4-4ec1-86ae-4e8cdc730d58")
	tierID     = uuid.MustParse("77680bc8-8bda-4375-8407-0cba078a0434")
)

func TestVerifyTokenRoundTrip(t *testing.T) {
	token := qr.GenerateToken(ticketID, purchaseID, tierID, testSecret)
	verifiedTicketID, err := qr.VerifyToken(token, testSecret)
	if err != nil {
		t.Fatalf("verify token: %v", err)
	}
	if ticketID != verifiedTicketID {
		t.Errorf("verified ticketID = %s, want %s", verifiedTicketID, ticketID)
	}
}

func signPayload(payload, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(payload))

	signed := fmt.Appendf(nil, "%s%s%x", payload, ".", h.Sum(nil))
	return base64.RawURLEncoding.EncodeToString(signed)
}

// test helper that returns a token whose payload has been edited but
// whose original signature is kept
func tamperedToken(t *testing.T) string {
	t.Helper()

	token := qr.GenerateToken(ticketID, purchaseID, tierID, testSecret)

	// decode the token so the payload can be edited
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatalf("tampered: decode generated token: %v", err)
	}
	payload, sig, _ := strings.Cut(string(decoded), ".")

	// replace the ticket ID part with another valid UUID (in this case tierID)
	parts := strings.Split(payload, ":")
	parts[0] = tierID.String()

	return base64.RawURLEncoding.EncodeToString(
		[]byte(strings.Join(parts, ":") + "." + sig),
	)
}

func TestVerifyTokenRejectsInvalidToken(t *testing.T) {
	validToken := qr.GenerateToken(ticketID, purchaseID, tierID, testSecret)

	tests := []struct {
		name   string
		token  string
		secret string
	}{
		{name: "wrong secret", token: validToken, secret: "fake_secret"},
		{name: "not base64", token: "!!!not-base64!!!", secret: testSecret},
		{name: "missing signature separator", token: "payload-with-no-dot", secret: testSecret},
		{name: "non hex signature", token: base64.RawURLEncoding.EncodeToString([]byte("payload.zzzz")), secret: testSecret},
		{name: "payload too few parts", token: signPayload("no-colon", testSecret), secret: testSecret},
		{name: "payload too many parts", token: signPayload("a:b:c:d", testSecret), secret: testSecret},
		{name: "non-UUID ticket ID", token: signPayload("not-a-uuid:purchase:tier", testSecret), secret: testSecret},
		{name: "tampered payload", token: tamperedToken(t), secret: testSecret},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := qr.VerifyToken(tt.token, tt.secret)
			if err == nil {
				t.Fatalf("%s: an error should be returned", tt.name)
			}
			if !errors.Is(err, qr.ErrInvalidToken) {
				t.Errorf("%s: got error %v, want %v", tt.name, err, qr.ErrInvalidToken)
			}
			if got != uuid.Nil {
				t.Errorf("%s: got ticketID %s, want %v", tt.name, got, uuid.Nil)
			}
		})
	}
}
