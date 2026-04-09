package jwt

import (
	"fmt"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"
)

// OperatorManager manages NATS operator JWT operations
type OperatorManager struct {
	operatorKP   nkeys.KeyPair
	operatorJWT  string
	operatorName string
}

// NewOperatorManager creates a new operator manager from an existing seed or generates a new one
func NewOperatorManager(seed []byte, operatorName string) (*OperatorManager, error) {
	var kp nkeys.KeyPair
	var err error

	if len(seed) > 0 {
		// Use existing seed
		kp, err = nkeys.FromSeed(seed)
		if err != nil {
			return nil, fmt.Errorf("failed to create keypair from seed: %w", err)
		}
	} else {
		// Generate new operator keypair
		kp, err = nkeys.CreateOperator()
		if err != nil {
			return nil, fmt.Errorf("failed to create operator keypair: %w", err)
		}
	}

	// Create operator claims
	pubKey, err := kp.PublicKey()
	if err != nil {
		return nil, fmt.Errorf("failed to get public key: %w", err)
	}

	claims := jwt.NewOperatorClaims(pubKey)
	claims.Name = operatorName
	claims.Issuer = pubKey
	claims.IssuedAt = time.Now().Unix()

	// Sign the operator JWT
	operatorJWT, err := claims.Encode(kp)
	if err != nil {
		return nil, fmt.Errorf("failed to encode operator JWT: %w", err)
	}

	return &OperatorManager{
		operatorKP:   kp,
		operatorJWT:  operatorJWT,
		operatorName: operatorName,
	}, nil
}

// GetPublicKey returns the operator's public key
func (om *OperatorManager) GetPublicKey() (string, error) {
	return om.operatorKP.PublicKey()
}

// GetSeed returns the operator's seed (private key)
func (om *OperatorManager) GetSeed() ([]byte, error) {
	return om.operatorKP.Seed()
}

// GetJWT returns the operator JWT
func (om *OperatorManager) GetJWT() string {
	return om.operatorJWT
}

// GetKeyPair returns the operator's keypair (for signing account JWTs)
func (om *OperatorManager) GetKeyPair() nkeys.KeyPair {
	return om.operatorKP
}

// SetSystemAccount re-encodes the operator JWT with the given account public key
// embedded as the SystemAccount field. Call this after the system account is known.
// The server will then show the system account name under "Trusted Operators" at startup
// instead of an empty string.
func (om *OperatorManager) SetSystemAccount(systemAccountPubKey string) error {
	pubKey, err := om.operatorKP.PublicKey()
	if err != nil {
		return fmt.Errorf("failed to get operator public key: %w", err)
	}

	claims := jwt.NewOperatorClaims(pubKey)
	claims.Name = om.operatorName
	claims.Issuer = pubKey
	claims.IssuedAt = time.Now().Unix()
	claims.SystemAccount = systemAccountPubKey

	token, err := claims.Encode(om.operatorKP)
	if err != nil {
		return fmt.Errorf("failed to re-encode operator JWT with system account: %w", err)
	}

	om.operatorJWT = token
	return nil
}

// SignAccountJWT signs an account JWT with the operator key
func (om *OperatorManager) SignAccountJWT(accountClaims *jwt.AccountClaims) (string, error) {
	// Set the issuer to the operator's public key
	pubKey, err := om.operatorKP.PublicKey()
	if err != nil {
		return "", fmt.Errorf("failed to get operator public key: %w", err)
	}
	accountClaims.Issuer = pubKey

	// Sign the account JWT
	token, err := accountClaims.Encode(om.operatorKP)
	if err != nil {
		return "", fmt.Errorf("failed to encode account JWT: %w", err)
	}

	return token, nil
}
