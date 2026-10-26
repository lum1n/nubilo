package identity_test

import (
	"context"
	"errors"
	"testing"
	"time"

	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/identity"
)

func TestInviteCodeFormat(t *testing.T) {
	code, err := identity.NewInviteCode()
	if err != nil {
		t.Fatal(err)
	}
	if len(code) != 24 || code[:4] != "nbi-" {
		t.Fatalf("code %q", code)
	}
	if identity.NormalizeInviteCode(code) == "" {
		t.Fatal("own code does not normalize")
	}
	lower := "NBI-" + code[4:]
	if identity.NormalizeInviteCode(lower) != identity.NormalizeInviteCode(code) {
		t.Fatal("prefix case matters")
	}
	if identity.NormalizeInviteCode("nbi-short") != "" {
		t.Fatal("short code accepted")
	}
}

func redeem(t *testing.T, s *identity.Service, code, username string) (*identity.RedeemResult, error) {
	t.Helper()
	pub, priv, _ := ncrypto.GenerateEd25519()
	res, err := s.RedeemInvite(t.Context(), code, identity.RedeemRequest{Username: username, DeviceName: "phone", PublicKey: pub})
	if err != nil {
		return nil, err
	}
	dev, err := s.Complete(t.Context(), res.PairingID, ncrypto.SignEd25519(priv, res.Challenge))
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if dev.UserID != res.User.ID {
		t.Fatalf("device user %s want %s", dev.UserID, res.User.ID)
	}
	return res, nil
}

func TestRedeemCreatesUser(t *testing.T) {
	s, ctx := setup(t)
	var created []string
	s.OnUserCreated = func(_ context.Context, u *identity.User) error { created = append(created, u.Username); return nil }
	code, inv, err := s.CreateInvite(ctx, "admin", identity.UserAdmin, "", time.Hour, 2)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Role != identity.UserAdmin || inv.MaxUses != 2 {
		t.Fatalf("invite %+v", inv)
	}
	res, err := redeem(t, s, code, "Alice")
	if err != nil {
		t.Fatal(err)
	}
	if !res.NewUser || res.User.Username != "alice" || res.User.Role != identity.UserAdmin {
		t.Fatalf("user %+v", res.User)
	}
	if len(created) != 1 {
		t.Fatalf("OnUserCreated calls %v", created)
	}
	if _, err := redeem(t, s, code, "alice"); !errors.Is(err, identity.ErrUserExists) {
		t.Fatalf("duplicate username: %v", err)
	}
	// The failed attempt rolled back, so one use remains.
	if _, err := redeem(t, s, code, "bob"); err != nil {
		t.Fatalf("second use: %v", err)
	}
	if _, err := redeem(t, s, code, "carol"); !errors.Is(err, identity.ErrInviteInvalid) {
		t.Fatalf("max uses: %v", err)
	}
}

func TestInviteForExistingUser(t *testing.T) {
	s, ctx := setup(t)
	u, err := s.CreateUser(ctx, "dana", "Dana", identity.UserMember)
	if err != nil {
		t.Fatal(err)
	}
	code, _, err := s.CreateInvite(ctx, "admin", identity.UserAdmin, u.ID, time.Hour, 1)
	if err != nil {
		t.Fatal(err)
	}
	res, err := redeem(t, s, code, "ignored")
	if err != nil {
		t.Fatal(err)
	}
	if res.NewUser || res.User.ID != u.ID || res.User.Role != identity.UserMember {
		t.Fatalf("bound invite %+v new=%v", res.User, res.NewUser)
	}
}

func TestInviteExpiryAndRevoke(t *testing.T) {
	s, ctx := setup(t)
	code, inv, err := s.CreateInvite(ctx, "admin", identity.UserMember, "", time.Hour, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeInvite(ctx, inv.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := redeem(t, s, code, "x"); !errors.Is(err, identity.ErrInviteInvalid) {
		t.Fatalf("revoked invite: %v", err)
	}
	code, _, _ = s.CreateInvite(ctx, "admin", identity.UserMember, "", time.Millisecond, 1)
	time.Sleep(5 * time.Millisecond)
	if _, err := redeem(t, s, code, "y"); !errors.Is(err, identity.ErrInviteInvalid) {
		t.Fatalf("expired invite: %v", err)
	}
	if _, err := redeem(t, s, "nbi-AAAAAAAAAAAAAAAAAAAA", "z"); !errors.Is(err, identity.ErrInviteInvalid) {
		t.Fatalf("unknown code: %v", err)
	}
}

func TestUsernameRules(t *testing.T) {
	for _, bad := range []string{"", "owner", "Admin", "-x", "a b", "æøå", "01ARZ3NDEKTSV4RRFFQ69G5FAV", "toolongtoolongtoolongtoolongtoolong"} {
		if _, err := identity.NormalizeUsername(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	if got, err := identity.NormalizeUsername(" Kari.Nordmann "); err != nil || got != "kari.nordmann" {
		t.Fatalf("got %q %v", got, err)
	}
}

func TestBasicAuthByUsername(t *testing.T) {
	s, ctx := setup(t)
	u, _ := s.CreateUser(ctx, "erik", "", identity.UserMember)
	other, _ := s.CreateUser(ctx, "frida", "", identity.UserMember)
	dev, pw, err := s.CreateAppPassword(ctx, u.ID, "iPhone", "caldav")
	if err != nil {
		t.Fatal(err)
	}
	_, pw2, _ := s.CreateAppPassword(ctx, u.ID, "Mac", "webdav")
	got, err := s.AuthenticateBasic(ctx, "erik", pw)
	if err != nil || got.ID != dev.ID {
		t.Fatalf("username login: %v", err)
	}
	if got, err := s.AuthenticateBasic(ctx, "ERIK", pw2); err != nil || got.ID == dev.ID {
		t.Fatalf("second password picks its own device: %v", err)
	}
	if _, err := s.AuthenticateBasic(ctx, dev.ID, pw); err != nil {
		t.Fatalf("legacy device-id login: %v", err)
	}
	if _, err := s.AuthenticateBasic(ctx, "frida", pw); err == nil {
		t.Fatal("erik's password accepted for frida")
	}
	_ = other
	// Revocation beats the success cache.
	if err := s.RevokeAppPassword(ctx, u.ID, dev.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateBasic(ctx, "erik", pw); err == nil {
		t.Fatal("revoked password still accepted")
	}
	// Disabling the user ends all sessions.
	if err := s.SetUserStatus(ctx, u.ID, identity.UserDisabled); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateBasic(ctx, "erik", pw2); err == nil {
		t.Fatal("disabled user accepted")
	}
}

func TestAppPasswordCap(t *testing.T) {
	s, ctx := setup(t)
	u, _ := s.CreateUser(ctx, "gina", "", identity.UserMember)
	for i := 0; i < identity.MaxAppPasswords; i++ {
		if _, _, err := s.CreateAppPassword(ctx, u.ID, "p", "webdav"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.CreateAppPassword(ctx, u.ID, "p", "webdav"); !errors.Is(err, identity.ErrTooManyPasswords) {
		t.Fatalf("cap: %v", err)
	}
}

func TestPairingBoundToUser(t *testing.T) {
	s, ctx := setup(t)
	u, _ := s.CreateUser(ctx, "hans", "", identity.UserMember)
	code, _, err := s.StartPairing(ctx, u.ID, identity.RoleClient)
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ncrypto.GenerateEd25519()
	b, err := s.Begin(ctx, identity.BeginRequest{Code: code, Name: "laptop", PublicKey: pub})
	if err != nil {
		t.Fatal(err)
	}
	dev, err := s.Complete(ctx, b.PairingID, ncrypto.SignEd25519(priv, b.Challenge))
	if err != nil {
		t.Fatal(err)
	}
	if dev.UserID != u.ID || dev.UserRole != identity.UserMember || dev.IsAdmin() {
		t.Fatalf("device %+v", dev)
	}
	// Without a user, pairing lands on the owner admin.
	code, _, _ = s.StartPairing(ctx, "", identity.RoleAgent)
	pub2, priv2, _ := ncrypto.GenerateEd25519()
	b, _ = s.Begin(ctx, identity.BeginRequest{Code: code, Name: "mac", PublicKey: pub2})
	dev, err = s.Complete(ctx, b.PairingID, ncrypto.SignEd25519(priv2, b.Challenge))
	if err != nil {
		t.Fatal(err)
	}
	owner, _ := s.GetUserByName(ctx, "owner")
	if owner == nil || dev.UserID != owner.ID || !dev.IsAdmin() {
		t.Fatalf("default pairing user %+v", dev)
	}
}

func TestLastAdminProtected(t *testing.T) {
	s, ctx := setup(t)
	a, _ := s.CreateUser(ctx, "ivar", "", identity.UserAdmin)
	if err := s.SetUserRole(ctx, a.ID, identity.UserMember); !errors.Is(err, identity.ErrLastAdmin) {
		t.Fatalf("demote last admin: %v", err)
	}
	if err := s.SetUserStatus(ctx, a.ID, identity.UserDisabled); !errors.Is(err, identity.ErrLastAdmin) {
		t.Fatalf("disable last admin: %v", err)
	}
	b, _ := s.CreateUser(ctx, "jon", "", identity.UserAdmin)
	if err := s.SetUserRole(ctx, a.ID, identity.UserMember); err != nil {
		t.Fatal(err)
	}
	_ = b
}
