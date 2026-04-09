package jwt

import (
	"strings"
	"testing"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"

	natsv1alpha1 "github.com/jradikk/nats-auth-operator/api/v1alpha1"
)

// account keypair must be stable — same seed always produces the same public key.
func TestAccountKeypairStability(t *testing.T) {
	// Generate a seed once
	kp, err := nkeys.CreateAccount()
	if err != nil {
		t.Fatalf("failed to create account keypair: %v", err)
	}
	seed, err := kp.Seed()
	if err != nil {
		t.Fatalf("failed to get seed: %v", err)
	}
	wantPubKey, err := kp.PublicKey()
	if err != nil {
		t.Fatalf("failed to get public key: %v", err)
	}

	// Reconstruct the manager from the same seed multiple times
	for i := 0; i < 3; i++ {
		am, err := NewAccountManager(seed)
		if err != nil {
			t.Fatalf("NewAccountManager() error = %v", err)
		}
		gotPubKey, err := am.GetPublicKey()
		if err != nil {
			t.Fatalf("GetPublicKey() error = %v", err)
		}
		if gotPubKey != wantPubKey {
			t.Errorf("iteration %d: public key changed: got %q, want %q", i, gotPubKey, wantPubKey)
		}
	}
}

// account JWT must reflect updated limits (including JetStream).
func TestAccountClaimsReflectLimits(t *testing.T) {
	am, err := NewAccountManager(nil)
	if err != nil {
		t.Fatalf("NewAccountManager() error = %v", err)
	}

	limits := &natsv1alpha1.AccountLimits{
		Conn:            50,
		Subs:            500,
		Payload:         1048576,
		Data:            -1,
		Exports:         10,
		Imports:         10,
		WildcardExports: true,
		JetStream: &natsv1alpha1.JetStreamLimits{
			MemoryStorage:        -1,
			DiskStorage:          -1,
			Streams:              20,
			Consumer:             100,
			MaxAckPending:        -1,
			MemoryMaxStreamBytes: -1,
			DiskMaxStreamBytes:   -1,
			MaxBytesRequired:     false,
		},
	}

	claims, err := am.CreateAccountClaims("test-account", "desc", limits)
	if err != nil {
		t.Fatalf("CreateAccountClaims() error = %v", err)
	}

	if claims.Limits.Conn != 50 {
		t.Errorf("Conn: got %d, want 50", claims.Limits.Conn)
	}
	if claims.Limits.Streams != 20 {
		t.Errorf("Streams: got %d, want 20", claims.Limits.Streams)
	}
	if claims.Limits.Consumer != 100 {
		t.Errorf("Consumer: got %d, want 100", claims.Limits.Consumer)
	}
	if claims.Limits.DiskStorage != -1 {
		t.Errorf("DiskStorage: got %d, want -1", claims.Limits.DiskStorage)
	}
}

// account JWT with nil limits must not panic and produce valid claims.
func TestAccountClaimsNilLimits(t *testing.T) {
	am, err := NewAccountManager(nil)
	if err != nil {
		t.Fatalf("NewAccountManager() error = %v", err)
	}
	claims, err := am.CreateAccountClaims("no-limits", "", nil)
	if err != nil {
		t.Fatalf("CreateAccountClaims() error = %v", err)
	}
	if claims.Name != "no-limits" {
		t.Errorf("Name: got %q, want %q", claims.Name, "no-limits")
	}
}

// user JWT must carry updated publish/subscribe permissions.
func TestUserClaimsReflectPermissions(t *testing.T) {
	um, err := NewUserManager(nil)
	if err != nil {
		t.Fatalf("NewUserManager() error = %v", err)
	}

	perms := &natsv1alpha1.Permissions{
		PublishAllow:   []string{"app.>", "$JS.ACK.>"},
		PublishDeny:    []string{"system.>"},
		SubscribeAllow: []string{"app.>", "_INBOX.>"},
		SubscribeDeny:  []string{"$SYS.>"},
	}

	claims, err := um.CreateUserClaims("test-user", perms)
	if err != nil {
		t.Fatalf("CreateUserClaims() error = %v", err)
	}

	if !containsAll(claims.Pub.Allow, "app.>", "$JS.ACK.>") {
		t.Errorf("Pub.Allow missing expected subjects: %v", claims.Pub.Allow)
	}
	if !containsAll(claims.Pub.Deny, "system.>") {
		t.Errorf("Pub.Deny missing expected subjects: %v", claims.Pub.Deny)
	}
	if !containsAll(claims.Sub.Allow, "app.>", "_INBOX.>") {
		t.Errorf("Sub.Allow missing expected subjects: %v", claims.Sub.Allow)
	}
	if !containsAll(claims.Sub.Deny, "$SYS.>") {
		t.Errorf("Sub.Deny missing expected subjects: %v", claims.Sub.Deny)
	}
}

// updated permissions produce a different signed JWT when re-signed.
func TestUserJWTChangesWhenPermissionsChange(t *testing.T) {
	seed := generateTestUserSeed(t)

	// Sign with original permissions
	um1, _ := NewUserManager(seed)
	claims1, _ := um1.CreateUserClaims("u", &natsv1alpha1.Permissions{
		PublishAllow: []string{"old.>"},
	})
	am, _ := NewAccountManager(nil)
	jwt1, err := am.SignUserJWT(claims1)
	if err != nil {
		t.Fatalf("SignUserJWT() error = %v", err)
	}

	// Sign with updated permissions (using same user seed → same user pubkey)
	um2, _ := NewUserManager(seed)
	claims2, _ := um2.CreateUserClaims("u", &natsv1alpha1.Permissions{
		PublishAllow: []string{"old.>", "new.>"},
	})
	jwt2, err := am.SignUserJWT(claims2)
	if err != nil {
		t.Fatalf("SignUserJWT() error = %v", err)
	}

	if jwt1 == jwt2 {
		t.Error("JWT did not change after permissions update")
	}
}

// user keypair must be stable — same seed always produces the same public key.
func TestUserKeypairStability(t *testing.T) {
	seed := generateTestUserSeed(t)

	kp, _ := nkeys.FromSeed(seed)
	wantPubKey, _ := kp.PublicKey()

	for i := 0; i < 3; i++ {
		um, err := NewUserManager(seed)
		if err != nil {
			t.Fatalf("NewUserManager() error = %v", err)
		}
		gotPubKey, err := um.GetPublicKey()
		if err != nil {
			t.Fatalf("GetPublicKey() error = %v", err)
		}
		if gotPubKey != wantPubKey {
			t.Errorf("iteration %d: user public key changed: got %q, want %q", i, gotPubKey, wantPubKey)
		}
	}
}

// user JWT signed by account must decode with correct issuer.
func TestUserJWTIssuerMatchesAccount(t *testing.T) {
	am, _ := NewAccountManager(nil)
	accountPubKey, _ := am.GetPublicKey()

	um, _ := NewUserManager(nil)
	claims, _ := um.CreateUserClaims("u", nil)
	signed, err := am.SignUserJWT(claims)
	if err != nil {
		t.Fatalf("SignUserJWT() error = %v", err)
	}

	decoded, err := jwt.DecodeUserClaims(signed)
	if err != nil {
		t.Fatalf("DecodeUserClaims() error = %v", err)
	}
	if decoded.Issuer != accountPubKey {
		t.Errorf("Issuer: got %q, want %q", decoded.Issuer, accountPubKey)
	}
}

// GenerateCredsFile must produce output the NATS client SDK can parse:
// two PEM-like blocks in order (JWT then seed), with the exact headers the SDK expects.
func TestGenerateCredsFileFormat(t *testing.T) {
	um, _ := NewUserManager(nil)
	am, _ := NewAccountManager(nil)
	claims, _ := um.CreateUserClaims("u", nil)
	userJWT, _ := am.SignUserJWT(claims)
	seed, _ := um.GetSeed()

	creds := GenerateCredsFile(userJWT, seed)

	// NATS client SDK looks for these exact delimiters
	requiredStrings := []string{
		"-----BEGIN NATS USER JWT-----",
		"------END NATS USER JWT------",
		"-----BEGIN USER NKEY SEED-----",
		"------END USER NKEY SEED------",
		userJWT,
		string(seed),
	}
	for _, s := range requiredStrings {
		if !strings.Contains(creds, s) {
			t.Errorf("creds file missing expected string %q", s)
		}
	}

	// JWT block must come before seed block
	jwtPos := strings.Index(creds, "-----BEGIN NATS USER JWT-----")
	seedPos := strings.Index(creds, "-----BEGIN USER NKEY SEED-----")
	if jwtPos >= seedPos {
		t.Error("JWT block must appear before seed block in creds file")
	}
}

// SignUserJWT round-trip: decoded JWT must have correct Subject (user pubkey),
// Issuer (account pubkey), and permissions intact.
func TestSignUserJWTRoundTrip(t *testing.T) {
	am, _ := NewAccountManager(nil)
	accountPubKey, _ := am.GetPublicKey()

	um, _ := NewUserManager(nil)
	userPubKey, _ := um.GetPublicKey()

	perms := &natsv1alpha1.Permissions{
		PublishAllow:   []string{"app.>", "$JS.ACK.>"},
		PublishDeny:    []string{"denied.>"},
		SubscribeAllow: []string{"app.>", "_INBOX.>"},
		SubscribeDeny:  []string{"$SYS.>"},
	}
	claims, _ := um.CreateUserClaims("myuser", perms)
	signed, err := am.SignUserJWT(claims)
	if err != nil {
		t.Fatalf("SignUserJWT() error = %v", err)
	}

	decoded, err := jwt.DecodeUserClaims(signed)
	if err != nil {
		t.Fatalf("DecodeUserClaims() error = %v", err)
	}

	if decoded.Subject != userPubKey {
		t.Errorf("Subject: got %q, want %q", decoded.Subject, userPubKey)
	}
	if decoded.Issuer != accountPubKey {
		t.Errorf("Issuer: got %q, want %q", decoded.Issuer, accountPubKey)
	}
	if decoded.Name != "myuser" {
		t.Errorf("Name: got %q, want %q", decoded.Name, "myuser")
	}
	if !containsAll(decoded.Pub.Allow, "app.>", "$JS.ACK.>") {
		t.Errorf("Pub.Allow: got %v", decoded.Pub.Allow)
	}
	if !containsAll(decoded.Pub.Deny, "denied.>") {
		t.Errorf("Pub.Deny: got %v", decoded.Pub.Deny)
	}
	if !containsAll(decoded.Sub.Allow, "app.>", "_INBOX.>") {
		t.Errorf("Sub.Allow: got %v", decoded.Sub.Allow)
	}
	if !containsAll(decoded.Sub.Deny, "$SYS.>") {
		t.Errorf("Sub.Deny: got %v", decoded.Sub.Deny)
	}
}

// helpers

func containsAll(list jwt.StringList, items ...string) bool {
	set := make(map[string]bool, len(list))
	for _, s := range list {
		set[s] = true
	}
	for _, item := range items {
		if !set[item] {
			return false
		}
	}
	return true
}

func generateTestUserSeed(t *testing.T) []byte {
	t.Helper()
	kp, err := nkeys.CreateUser()
	if err != nil {
		t.Fatalf("failed to create user keypair: %v", err)
	}
	seed, err := kp.Seed()
	if err != nil {
		t.Fatalf("failed to get seed: %v", err)
	}
	return seed
}
