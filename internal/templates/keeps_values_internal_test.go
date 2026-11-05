package templates

import (
	"encoding/base64"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/inventory/schema"
)

// TestKeepsValuesOverAnEnvelopeThatDoesNotOpen pins the settings save's
// comparison (buildKeepingValues): a stored plain secret and a sealed
// envelope are the same value only when the envelope opens under the
// template's own AAD to exactly that string. An envelope that does not
// open — sealed under another template, by another key, or not ciphertext
// at all — is a changed value, never an error and never the same. The
// assertions are booleans only: nothing a box opens is printed.
func TestKeepsValuesOverAnEnvelopeThatDoesNotOpen(t *testing.T) {
	box := crypto.NewBox(keepsKey("keeps-values-box"))
	otherBox := crypto.NewBox(keepsKey("keeps-values-other-box"))
	s := NewService(nil, nil, nil, nil, nil, box)

	org := uuid.New()
	aad := ExtraVarsAAD(org, uuid.New())
	otherAAD := ExtraVarsAAD(org, uuid.New())

	seal := func(t *testing.T, b *crypto.Box, plain string, aad []byte) map[string]any {
		t.Helper()
		ct, err := b.SealAAD([]byte(plain), aad)
		if err != nil {
			t.Fatal(err)
		}
		return schema.Sealed(ct)
	}
	garbage := map[string]any{"$sealed": base64.StdEncoding.EncodeToString([]byte("garbage"))}

	cases := []struct {
		name      string
		cur, next map[string]any
		want      bool
	}{
		{"an envelope that opens to the stored string",
			map[string]any{"token": "hunter2"}, map[string]any{"token": seal(t, box, "hunter2", aad)}, true},
		{"an envelope sealed under another template's AAD",
			map[string]any{"token": "hunter2"}, map[string]any{"token": seal(t, box, "hunter2", otherAAD)}, false},
		{"an envelope sealed by another key",
			map[string]any{"token": "hunter2"}, map[string]any{"token": seal(t, otherBox, "hunter2", aad)}, false},
		{"an envelope that is not ciphertext",
			map[string]any{"token": "hunter2"}, map[string]any{"token": garbage}, false},
		{"an envelope that opens to another string",
			map[string]any{"token": "hunter2"}, map[string]any{"token": seal(t, box, "hunter3", aad)}, false},
		{"a repeater item's envelope that opens to the stored string",
			map[string]any{"vhosts": []any{map[string]any{"password": "x"}}},
			map[string]any{"vhosts": []any{map[string]any{"password": seal(t, box, "x", aad)}}}, true},
		{"a repeater item's envelope that does not open",
			map[string]any{"vhosts": []any{map[string]any{"password": "x"}}},
			map[string]any{"vhosts": []any{map[string]any{"password": seal(t, box, "x", otherAAD)}}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := s.keepsValues(aad, c.cur, c.next); got != c.want {
				t.Fatalf("keepsValues = %v, want %v", got, c.want)
			}
		})
	}
}

func keepsKey(label string) [32]byte {
	var key [32]byte
	copy(key[:], label)
	return key
}
