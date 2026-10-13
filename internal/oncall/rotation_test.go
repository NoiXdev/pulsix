package oncall

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestValidate(t *testing.T) {
	base := func() Input {
		return Input{
			Name:       "db team",
			PeriodDays: 7,
			Anchor:     time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC),
			Timezone:   "Europe/Berlin",
			Channels:   []uuid.UUID{uuid.New(), uuid.New()},
		}
	}
	tests := []struct {
		name string
		mut  func(*Input)
		want bool
	}{
		{"the base case", func(*Input) {}, true},
		{"one channel is a degenerate but legal cycle", func(in *Input) {
			in.Channels = in.Channels[:1]
		}, true},
		{"blank name", func(in *Input) { in.Name = "  " }, false},
		{"no channel at all", func(in *Input) { in.Channels = nil }, false},
		{"a channel twice", func(in *Input) {
			c := uuid.New()
			in.Channels = []uuid.UUID{c, c}
		}, false},
		{"zero period", func(in *Input) { in.PeriodDays = 0 }, false},
		{"negative period", func(in *Input) { in.PeriodDays = -7 }, false},
		{"unknown timezone", func(in *Input) { in.Timezone = "Mars/Olympus" }, false},
		// Finding 3: time.LoadLocation("") returns UTC with no error, so
		// without this check an unset timezone would be accepted silently.
		{"blank timezone", func(in *Input) { in.Timezone = "" }, false},
		{"zero anchor", func(in *Input) { in.Anchor = time.Time{} }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := base()
			tc.mut(&in)
			err := Validate(in)
			if tc.want && err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
			if !tc.want {
				if err == nil {
					t.Error("Validate() = nil, want an error")
				} else if !errors.Is(err, ErrInvalidInput) {
					t.Errorf("Validate() = %v, want it to wrap ErrInvalidInput", err)
				}
			}
		})
	}
}

func TestValidateException(t *testing.T) {
	// Review Focus 5. A zero-length span matches no instant at all, and an
	// inverted one matches none either — both read in the GUI as a
	// working stand-in, which is the failure this rejects.
	at := time.Date(2026, 3, 3, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		from  time.Time
		to    time.Time
		valid bool
	}{
		{"an ordinary span", at, at.Add(48 * time.Hour), true},
		{"zero length", at, at, false},
		{"inverted", at.Add(48 * time.Hour), at, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateException(ExceptionInput{
				StartsAt: tc.from, EndsAt: tc.to, ChannelID: uuid.New(),
			})
			if tc.valid != (err == nil) {
				t.Errorf("ValidateException() = %v, want valid=%v", err, tc.valid)
			}
		})
	}
}
