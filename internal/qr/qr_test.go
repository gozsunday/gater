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
	genToken := qr.GenerateToken(ticketID, purchaseID, tierID, testSecret)
	verifiedTicketID, err := qr.VerifyToken(genToken, testSecret)
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

	signed := fmt.Appendf(nil, "%s%x", ".", h.Sum(nil))
	return base64.RawURLEncoding.EncodeToString(signed)
}

func TestVerifyTokenWrongSecret(t *testing.T) {
	genToken := qr.GenerateToken(ticketID, purchaseID, tierID, testSecret)
	verifiedTicketID, err := qr.VerifyToken(genToken, "fake_secret")
	if err == nil {
		t.Fatalf("wrong secret: an error should be returned")
	}
	if !errors.Is(err, qr.ErrInvalidToken) {
		t.Errorf("wrong secret: got error %v, want %v", err, qr.ErrInvalidToken)
	}
	if verifiedTicketID != uuid.Nil {
		t.Errorf("wrong secret: got ticketID %s, want %v", verifiedTicketID, uuid.Nil)
	}
}

func TestVerifyTokenTamperedPayload(t *testing.T) {
	genToken := qr.GenerateToken(ticketID, purchaseID, tierID, testSecret)

	// decode the token so the payload can be edited
	decoded, err := base64.RawURLEncoding.DecodeString(genToken)
	if err != nil {
		t.Fatalf("tampered: decode generated token: %v", err)
	}
	payload, sig, _ := strings.Cut(string(decoded), ".")

	// replace the ticket ID part with another valid UUID (in this case tierID)
	parts := strings.Split(payload, ":")
	parts[0] = tierID.String()
	tampered := base64.RawURLEncoding.EncodeToString(
		[]byte(strings.Join(parts, ":") + "." + sig),
	)

	verifiedTicketID, err := qr.VerifyToken(tampered, testSecret)
	if err == nil {
		t.Fatalf("tampered: an error should be returned")
	}
	if !errors.Is(err, qr.ErrInvalidToken) {
		t.Errorf("tampered: got error %v, want %v", err, qr.ErrInvalidToken)
	}
	if verifiedTicketID != uuid.Nil {
		t.Errorf("tampered: got ticketID %s, want %v", verifiedTicketID, uuid.Nil)
	}
}

func TestVerifyTokenNotBase64(t *testing.T) {
	token := "!!!not-base64!!!"
	verifiedTicketID, err := qr.VerifyToken(token, testSecret)
	if err == nil {
		t.Fatalf("not base64: an error should be returned")
	}
	if !errors.Is(err, qr.ErrInvalidToken) {
		t.Errorf("not base64: got error %v, want %v", err, qr.ErrInvalidToken)
	}
	if verifiedTicketID != uuid.Nil {
		t.Errorf("not base64: got ticketID %s, want %v", verifiedTicketID, uuid.Nil)
	}
}

func TestVerifyTokenMissingSignatureSeparator(t *testing.T) {
	token := "payload-with-no-dot"
	verifiedTicketID, err := qr.VerifyToken(token, testSecret)
	if err == nil {
		t.Fatalf("missing signature separator: an error should be returned")
	}
	if !errors.Is(err, qr.ErrInvalidToken) {
		t.Errorf("missing signature separator: got error %v, want %v", err, qr.ErrInvalidToken)
	}
	if verifiedTicketID != uuid.Nil {
		t.Errorf("missing signature separator: got ticketID %s, want %v", verifiedTicketID, uuid.Nil)
	}
}

func TestVerifyTokenNonHexSignature(t *testing.T) {
	token := base64.RawURLEncoding.EncodeToString([]byte("payload.zzzz"))
	verifiedTicketID, err := qr.VerifyToken(token, testSecret)
	if err == nil {
		t.Fatalf("non hex signature: an error should be returned")
	}
	if !errors.Is(err, qr.ErrInvalidToken) {
		t.Errorf("non hex signature: got error %v, want %v", err, qr.ErrInvalidToken)
	}
	if verifiedTicketID != uuid.Nil {
		t.Errorf("non hex signature: got ticketID %s, want %v", verifiedTicketID, uuid.Nil)
	}
}

func TestVerifyTokenPayloadWrongPartCount(t *testing.T) {
	token := signPayload("no-colon", testSecret)
	verifiedTicketID, err := qr.VerifyToken(token, testSecret)
	if err == nil {
		t.Fatalf("wrong part count: an error should be returned")
	}
	if !errors.Is(err, qr.ErrInvalidToken) {
		t.Errorf("wrong part count: got error %v, want %v", err, qr.ErrInvalidToken)
	}
	if verifiedTicketID != uuid.Nil {
		t.Errorf("wrong part count: got ticketID %s, want %v", verifiedTicketID, uuid.Nil)
	}
}

func TestVerifyTokenPayloadTooManyParts(t *testing.T) {
	token := signPayload("a:b:c:d", testSecret)
	verifiedTicketID, err := qr.VerifyToken(token, testSecret)
	if err == nil {
		t.Fatalf("too many parts: an error should be returned")
	}
	if !errors.Is(err, qr.ErrInvalidToken) {
		t.Errorf("too many parts: got error %v, want %v", err, qr.ErrInvalidToken)
	}
	if verifiedTicketID != uuid.Nil {
		t.Errorf("too many parts: got ticketID %s, want %v", verifiedTicketID, uuid.Nil)
	}
}

func TestVerifyTokenNonUUIDTicketID(t *testing.T) {
	token := signPayload("not-a-uuid:purchase:tier", testSecret)
	verifiedTicketID, err := qr.VerifyToken(token, testSecret)
	if err == nil {
		t.Fatalf("non-UUID ticketID: an error should be returned")
	}
	if !errors.Is(err, qr.ErrInvalidToken) {
		t.Errorf("non-UUID ticketID: got error %v, want %v", err, qr.ErrInvalidToken)
	}
	if verifiedTicketID != uuid.Nil {
		t.Errorf("non-UUID ticketID: got ticketID %s, want %v", verifiedTicketID, uuid.Nil)
	}
}
