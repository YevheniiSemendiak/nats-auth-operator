package jwt

import (
	"testing"

	natsjwt "github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"
)

func TestNewOperatorManager(t *testing.T) {
	tests := []struct {
		name         string
		seed         []byte
		operatorName string
		wantErr      bool
	}{
		{
			name:         "Create new operator",
			seed:         nil,
			operatorName: "Test Operator",
			wantErr:      false,
		},
		{
			name:         "Create from existing seed",
			seed:         generateTestOperatorSeed(t),
			operatorName: "Test Operator",
			wantErr:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			om, err := NewOperatorManager(tt.seed, tt.operatorName)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewOperatorManager() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if om == nil {
					t.Error("NewOperatorManager() returned nil")
					return
				}

				// Verify we can get the public key
				pubKey, err := om.GetPublicKey()
				if err != nil {
					t.Errorf("GetPublicKey() error = %v", err)
				}
				if pubKey == "" {
					t.Error("GetPublicKey() returned empty string")
				}

				// Verify we can get the JWT
				jwt := om.GetJWT()
				if jwt == "" {
					t.Error("GetJWT() returned empty string")
				}

				// Verify we can get the seed
				seed, err := om.GetSeed()
				if err != nil {
					t.Errorf("GetSeed() error = %v", err)
				}
				if len(seed) == 0 {
					t.Error("GetSeed() returned empty seed")
				}
			}
		})
	}
}

func TestOperatorManager_SignAccountJWT(t *testing.T) {
	om, err := NewOperatorManager(nil, "Test Operator")
	if err != nil {
		t.Fatalf("Failed to create operator manager: %v", err)
	}

	// Create a test account manager
	am, err := NewAccountManager(nil)
	if err != nil {
		t.Fatalf("Failed to create account manager: %v", err)
	}

	// Create account claims
	claims, err := am.CreateAccountClaims("Test Account", "Test Description", nil)
	if err != nil {
		t.Fatalf("Failed to create account claims: %v", err)
	}

	// Sign the account JWT
	jwt, err := om.SignAccountJWT(claims)
	if err != nil {
		t.Errorf("SignAccountJWT() error = %v", err)
	}
	if jwt == "" {
		t.Error("SignAccountJWT() returned empty JWT")
	}
}

// SetSystemAccount embeds the system account public key in the operator JWT.
func TestSetSystemAccount(t *testing.T) {
	om, err := NewOperatorManager(nil, "TestOperator")
	if err != nil {
		t.Fatalf("NewOperatorManager() error = %v", err)
	}

	// Before setting system account, JWT should have no SystemAccount
	before, err := natsjwt.DecodeOperatorClaims(om.GetJWT())
	if err != nil {
		t.Fatalf("DecodeOperatorClaims() before error = %v", err)
	}
	if before.SystemAccount != "" {
		t.Errorf("expected empty SystemAccount before SetSystemAccount, got %q", before.SystemAccount)
	}

	// Create a system account and get its public key
	sysAM, err := NewAccountManager(nil)
	if err != nil {
		t.Fatalf("NewAccountManager() error = %v", err)
	}
	sysPubKey, err := sysAM.GetPublicKey()
	if err != nil {
		t.Fatalf("GetPublicKey() error = %v", err)
	}

	if err := om.SetSystemAccount(sysPubKey); err != nil {
		t.Fatalf("SetSystemAccount() error = %v", err)
	}

	// Operator public key must be unchanged
	afterPubKey, err := om.GetPublicKey()
	if err != nil {
		t.Fatalf("GetPublicKey() after error = %v", err)
	}
	if afterPubKey != before.Issuer {
		t.Errorf("operator public key changed after SetSystemAccount: got %q, want %q", afterPubKey, before.Issuer)
	}

	// Decode updated JWT and verify SystemAccount is set
	after, err := natsjwt.DecodeOperatorClaims(om.GetJWT())
	if err != nil {
		t.Fatalf("DecodeOperatorClaims() after error = %v", err)
	}
	if after.SystemAccount != sysPubKey {
		t.Errorf("SystemAccount: got %q, want %q", after.SystemAccount, sysPubKey)
	}
}

// SetSystemAccount is idempotent — calling it again updates to the new key.
func TestSetSystemAccountIdempotent(t *testing.T) {
	om, err := NewOperatorManager(nil, "TestOperator")
	if err != nil {
		t.Fatalf("NewOperatorManager() error = %v", err)
	}

	sys1, _ := NewAccountManager(nil)
	key1, _ := sys1.GetPublicKey()
	if err := om.SetSystemAccount(key1); err != nil {
		t.Fatalf("first SetSystemAccount() error = %v", err)
	}

	sys2, _ := NewAccountManager(nil)
	key2, _ := sys2.GetPublicKey()
	if err := om.SetSystemAccount(key2); err != nil {
		t.Fatalf("second SetSystemAccount() error = %v", err)
	}

	decoded, err := natsjwt.DecodeOperatorClaims(om.GetJWT())
	if err != nil {
		t.Fatalf("DecodeOperatorClaims() error = %v", err)
	}
	if decoded.SystemAccount != key2 {
		t.Errorf("SystemAccount after second call: got %q, want %q", decoded.SystemAccount, key2)
	}
}

// operator keypair must be stable — same seed always produces the same public key.
func TestOperatorKeypairStability(t *testing.T) {
	seed := generateTestOperatorSeed(t)

	kp, _ := nkeys.FromSeed(seed)
	wantPubKey, _ := kp.PublicKey()

	for i := 0; i < 3; i++ {
		om, err := NewOperatorManager(seed, "TestOperator")
		if err != nil {
			t.Fatalf("NewOperatorManager() iteration %d error = %v", i, err)
		}
		gotPubKey, err := om.GetPublicKey()
		if err != nil {
			t.Fatalf("GetPublicKey() iteration %d error = %v", i, err)
		}
		if gotPubKey != wantPubKey {
			t.Errorf("iteration %d: operator public key changed: got %q, want %q", i, gotPubKey, wantPubKey)
		}
	}
}

func generateTestOperatorSeed(t *testing.T) []byte {
	kp, err := nkeys.CreateOperator()
	if err != nil {
		t.Fatalf("Failed to create test operator keypair: %v", err)
	}
	seed, err := kp.Seed()
	if err != nil {
		t.Fatalf("Failed to get seed: %v", err)
	}
	return seed
}
