package auth

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoginExpiryAndLogoutRevocationPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	st, err := Open(path, testUsers()...)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	user, token, expires, err := st.Login("prosecutor", "test-prosecutor-password", time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if user.Role != RoleProsecutor || !expires.Equal(now.Add(time.Hour)) {
		t.Fatalf("unexpected login result: %+v %s", user, expires)
	}
	encoded, err := json.Marshal(user)
	if err != nil || string(encoded) == "" || string(encoded) == "{}" {
		t.Fatalf("marshal user: %s (%v)", encoded, err)
	}
	if strings.Contains(string(encoded), "password_hash") {
		t.Fatalf("password hash leaked in user JSON: %s", encoded)
	}
	if got, err := st.Resolve(token, now.Add(30*time.Minute)); err != nil || got.ID != user.ID {
		t.Fatalf("resolve: %+v %v", got, err)
	}
	if err := st.Logout(token, now.Add(31*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Resolve(token, now.Add(32*time.Minute)); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("expected revoked, got %v", err)
	}
	st2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st2.Resolve(token, now.Add(32*time.Minute)); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("persisted revoke lost: %v", err)
	}
}

func TestExpiredSessionAndRoleGuard(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "auth.json"), testUsers()...)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	user, token, _, err := st.Login("counselor", "test-counselor-password", time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Resolve(token, now.Add(2*time.Minute)); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("expected expiry, got %v", err)
	}
	if err := RequireRole(user, RoleProsecutor); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected forbidden, got %v", err)
	}
}

func testUsers() []BootstrapUser {
	return []BootstrapUser{
		{ID: "u-admin", Username: "admin", Password: "test-admin-password", Role: RoleAdmin},
		{ID: "u-prosecutor", Username: "prosecutor", Password: "test-prosecutor-password", Role: RoleProsecutor},
		{ID: "u-counselor", Username: "counselor", Password: "test-counselor-password", Role: RoleCounselor},
	}
}
