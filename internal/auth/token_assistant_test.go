package auth_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

func TestAssistantTokenResolvesWithKindAndAreas(t *testing.T) {
	tk := auth.NewTokens(newMemTokenStore(), time.Now)
	uid, org := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	plain, tok, err := tk.CreateWith(context.Background(), uid, org, auth.NewToken{
		Name: "assistant", Role: "operator", Kind: auth.TokenKindAssistant,
		Areas: []string{"inventory.read", "jobs.read"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if tok.Kind != auth.TokenKindAssistant || !slices.Equal(tok.Areas, []string{"inventory.read", "jobs.read"}) {
		t.Fatalf("created token = %+v", tok)
	}
	_, scope, err := tk.Resolve(context.Background(), plain)
	if err != nil {
		t.Fatal(err)
	}
	if scope.Kind != auth.TokenKindAssistant || !slices.Equal(scope.Areas, []string{"inventory.read", "jobs.read"}) {
		t.Fatalf("scope = %+v", scope)
	}
}

func TestPositionalCreateMintsAnAPIToken(t *testing.T) {
	tk := auth.NewTokens(newMemTokenStore(), time.Now)
	plain, tok, err := tk.Create(context.Background(), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), "ci", "viewer", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, scope, err := tk.Resolve(context.Background(), plain)
	if err != nil || tok.Kind != auth.TokenKindAPI || scope.Kind != auth.TokenKindAPI || scope.Areas != nil {
		t.Fatalf("token %+v scope %+v err %v", tok, scope, err)
	}
}

func TestAssistantEndpointMarker(t *testing.T) {
	ctx := context.Background()
	if auth.ViaAssistantEndpoint(ctx) {
		t.Fatal("a bare context is marked")
	}
	if !auth.ViaAssistantEndpoint(auth.WithAssistantEndpoint(ctx)) {
		t.Fatal("the marker does not stick")
	}
}

func TestSQLTokenStoreKeepsKindAndAreas(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	uid := seedUser(t, q, "token-kind@example.test")
	org := seedOrg(t, q, "token-kind-org")
	store := auth.NewSQLTokenStore(d)

	asst := auth.Token{ID: uuid.Must(uuid.NewV7()), UserID: uid, OrgID: org, Name: "asst", Role: "operator",
		Kind: auth.TokenKindAssistant, Areas: []string{"repos.read"}}
	if err := store.Insert(ctx, asst, hashOf("kind-a")); err != nil {
		t.Fatal(err)
	}
	api := auth.Token{ID: uuid.Must(uuid.NewV7()), UserID: uid, OrgID: org, Name: "api", Role: "viewer", Kind: auth.TokenKindAPI}
	if err := store.Insert(ctx, api, hashOf("kind-b")); err != nil {
		t.Fatal(err)
	}
	got, _, err := store.GetByHash(ctx, hashOf("kind-a"), time.Now())
	if err != nil || got.Kind != auth.TokenKindAssistant || !slices.Equal(got.Areas, []string{"repos.read"}) {
		t.Fatalf("GetByHash = %+v, %v", got, err)
	}
	list, err := store.ListForOrg(ctx, org)
	if err != nil || len(list) != 2 {
		t.Fatalf("ListForOrg = %+v, %v", list, err)
	}
	for _, tok := range list {
		switch tok.ID {
		case asst.ID:
			if tok.Kind != auth.TokenKindAssistant || !slices.Equal(tok.Areas, []string{"repos.read"}) {
				t.Errorf("assistant row = %+v", tok)
			}
		case api.ID:
			if tok.Kind != auth.TokenKindAPI || tok.Areas != nil {
				t.Errorf("api row = %+v", tok)
			}
		}
	}
}

func TestSQLTokenStoreRefusesInconsistentAreas(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	uid := seedUser(t, q, "token-check@example.test")
	org := seedOrg(t, q, "token-check-org")
	store := auth.NewSQLTokenStore(d)
	noAreas := auth.Token{ID: uuid.Must(uuid.NewV7()), UserID: uid, OrgID: org, Name: "a", Role: "viewer", Kind: auth.TokenKindAssistant}
	if err := store.Insert(ctx, noAreas, hashOf("chk-a")); err == nil {
		t.Error("an assistant token without areas was stored")
	}
	apiAreas := auth.Token{ID: uuid.Must(uuid.NewV7()), UserID: uid, OrgID: org, Name: "b", Role: "viewer", Kind: auth.TokenKindAPI, Areas: []string{"jobs.read"}}
	if err := store.Insert(ctx, apiAreas, hashOf("chk-b")); err == nil {
		t.Error("an api token with areas was stored")
	}
}
