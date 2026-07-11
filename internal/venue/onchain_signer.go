package venue

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

var ErrSignerUnavailable = errors.New("external signer is unavailable")

// Signer keeps signing capability behind a narrow boundary. Implementations
// must never expose private key material through String or errors.
type Signer interface {
	Address() string
	SignTransaction(context.Context, map[string]any) ([]byte, error)
	fmt.Stringer
}

type SignTransactionFunc func(context.Context, map[string]any) ([]byte, error)

// ExternalSigner represents KMS, hardware-wallet, or injected test signing.
// identifier is retained privately and only a one-way short fingerprint is
// included in String output.
type ExternalSigner struct {
	kind       string
	address    string
	identifier string
	sign       SignTransactionFunc
}

func NewExternalSigner(
	kind, address, identifier string,
	sign SignTransactionFunc,
) (*ExternalSigner, error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	switch kind {
	case "kms", "hardware", "test":
	default:
		return nil, fmt.Errorf("unknown signer kind %q", kind)
	}
	if strings.TrimSpace(address) == "" {
		return nil, errors.New("signer address is required")
	}
	return &ExternalSigner{kind: kind, address: address, identifier: identifier, sign: sign}, nil
}

func (signer *ExternalSigner) Address() string { return signer.address }

func (signer *ExternalSigner) SignTransaction(
	ctx context.Context,
	transaction map[string]any,
) ([]byte, error) {
	if signer == nil || signer.sign == nil {
		return nil, ErrSignerUnavailable
	}
	return signer.sign(ctx, transaction)
}

func (signer *ExternalSigner) String() string {
	if signer == nil {
		return "ExternalSigner(<nil>)"
	}
	fingerprint := "none"
	if signer.identifier != "" {
		digest := sha256.Sum256([]byte(signer.identifier))
		fingerprint = hex.EncodeToString(digest[:4])
	}
	return fmt.Sprintf("ExternalSigner(kind=%s,address=%s,ref=%s)", signer.kind, signer.address, fingerprint)
}

func (signer *ExternalSigner) GoString() string { return signer.String() }
