package cronexpr

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Simple-mode kinds (spec §2): the form's input, compiled to cron on save.
const (
	SimpleEveryMinutes = "every_minutes"
	SimpleEveryHours   = "every_hours"
	SimpleDaily        = "daily"
	SimpleWeekly       = "weekly"
	SimpleMonthly      = "monthly"
)

// Simple is the simple-mode form input, stored verbatim so the form
// round-trips.
type Simple struct {
	Kind  string   `json:"kind"`
	Every int      `json:"every,omitempty"`
	Time  string   `json:"time,omitempty"`
	Days  []string `json:"days,omitempty"`
	Day   int      `json:"day,omitempty"`
}

var (
	minuteSteps = []int{1, 2, 3, 4, 5, 6, 10, 12, 15, 20, 30}
	hourSteps   = []int{1, 2, 3, 4, 6, 8, 12}
	dayOrder    = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}
	dayNumber   = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}
	timeRE      = regexp.MustCompile(`^([01]\d|2[0-3]):([0-5]\d)$`)
)

// Compile turns the form input into a cron expression.
func (s Simple) Compile() (string, error) {
	switch s.Kind {
	case SimpleEveryMinutes:
		if !slices.Contains(minuteSteps, s.Every) {
			return "", fmt.Errorf("%w: every must be one of %v minutes", ErrSyntax, minuteSteps)
		}
		if s.Every == 1 {
			return "* * * * *", nil
		}
		return fmt.Sprintf("*/%d * * * *", s.Every), nil
	case SimpleEveryHours:
		if !slices.Contains(hourSteps, s.Every) {
			return "", fmt.Errorf("%w: every must be one of %v hours", ErrSyntax, hourSteps)
		}
		if s.Every == 1 {
			return "0 * * * *", nil
		}
		return fmt.Sprintf("0 */%d * * *", s.Every), nil
	}
	h, m, err := clock(s.Time)
	if err != nil {
		return "", err
	}
	switch s.Kind {
	case SimpleDaily:
		return fmt.Sprintf("%d %d * * *", m, h), nil
	case SimpleWeekly:
		nums, err := weekdays(s.Days)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%d %d * * %s", m, h, nums), nil
	case SimpleMonthly:
		if s.Day < 1 || s.Day > 31 {
			return "", fmt.Errorf("%w: day must be 1-31", ErrSyntax)
		}
		return fmt.Sprintf("%d %d %d * *", m, h, s.Day), nil
	}
	return "", fmt.Errorf("%w: unknown simple kind %q", ErrSyntax, s.Kind)
}

// Describe renders the input as the short text the schedules list shows.
func (s Simple) Describe() string {
	switch s.Kind {
	case SimpleEveryMinutes:
		if s.Every == 1 {
			return "every minute"
		}
		return fmt.Sprintf("every %d min", s.Every)
	case SimpleEveryHours:
		if s.Every == 1 {
			return "every hour"
		}
		return fmt.Sprintf("every %d h", s.Every)
	case SimpleDaily:
		return "daily at " + s.Time
	case SimpleWeekly:
		names := make([]string, 0, len(s.Days))
		for _, d := range dayOrder {
			if slices.Contains(s.Days, d) {
				names = append(names, strings.ToUpper(d[:1])+d[1:])
			}
		}
		return strings.Join(names, ", ") + " at " + s.Time
	case SimpleMonthly:
		return fmt.Sprintf("monthly on day %d at %s", s.Day, s.Time)
	}
	return ""
}

var (
	everyMinRE = regexp.MustCompile(`^(\*|\*/(\d+)) \* \* \* \*$`)
	everyHrRE  = regexp.MustCompile(`^0 (\*|\*/(\d+)) \* \* \*$`)
	atRE       = regexp.MustCompile(`^(\d{1,2}) (\d{1,2}) (\*|\d{1,2}) \* (\*|[0-6](?:,[0-6])*)$`)
)

// FromCron recognizes the expressions simple mode produces (and the macros).
func FromCron(spec string) (Simple, bool) {
	e, err := Parse(spec)
	if err != nil {
		return Simple{}, false
	}
	s := e.spec
	if m, ok := macros[s]; ok {
		s = m
	}
	if m := everyMinRE.FindStringSubmatch(s); m != nil {
		n := 1
		if m[2] != "" {
			n, _ = strconv.Atoi(m[2])
		}
		out := Simple{Kind: SimpleEveryMinutes, Every: n}
		_, cerr := out.Compile()
		return out, cerr == nil
	}
	if m := everyHrRE.FindStringSubmatch(s); m != nil {
		n := 1
		if m[2] != "" {
			n, _ = strconv.Atoi(m[2])
		}
		out := Simple{Kind: SimpleEveryHours, Every: n}
		_, cerr := out.Compile()
		return out, cerr == nil
	}
	m := atRE.FindStringSubmatch(s)
	if m == nil {
		return Simple{}, false
	}
	minute, _ := strconv.Atoi(m[1])
	hour, _ := strconv.Atoi(m[2])
	at := fmt.Sprintf("%02d:%02d", hour, minute)
	switch {
	case m[3] == "*" && m[4] == "*":
		return Simple{Kind: SimpleDaily, Time: at}, true
	case m[3] == "*":
		var days []string
		for _, n := range strings.Split(m[4], ",") {
			v, _ := strconv.Atoi(n)
			for name, num := range dayNumber {
				if num == v {
					days = append(days, name)
				}
			}
		}
		out := Simple{Kind: SimpleWeekly, Days: days, Time: at}
		// Keep FromCron's contract uniform with the every_minutes/
		// every_hours branches above: only recognize a shape that Compile
		// itself would accept back (a duplicated day, e.g. "2,2", would
		// otherwise round-trip to a Describe text that Compile rejects).
		_, cerr := out.Compile()
		return out, cerr == nil
	case m[4] == "*":
		d, _ := strconv.Atoi(m[3])
		return Simple{Kind: SimpleMonthly, Day: d, Time: at}, true
	}
	return Simple{}, false
}

// Describe returns human text for a cron expression of a simple shape and
// "" for everything else (the UI then shows the expression alone).
func Describe(spec string) string {
	s, ok := FromCron(spec)
	if !ok {
		return ""
	}
	return s.Describe()
}

func clock(v string) (hour, minute int, err error) {
	m := timeRE.FindStringSubmatch(v)
	if m == nil {
		return 0, 0, fmt.Errorf("%w: time must be HH:MM (00:00-23:59)", ErrSyntax)
	}
	hour, _ = strconv.Atoi(m[1])
	minute, _ = strconv.Atoi(m[2])
	return hour, minute, nil
}

func weekdays(days []string) (string, error) {
	if len(days) == 0 {
		return "", fmt.Errorf("%w: pick at least one day", ErrSyntax)
	}
	seen := map[string]bool{}
	for _, d := range days {
		if _, ok := dayNumber[d]; !ok || seen[d] {
			return "", fmt.Errorf("%w: days must be distinct values of mon..sun", ErrSyntax)
		}
		seen[d] = true
	}
	var nums []string
	for _, d := range []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"} {
		if seen[d] {
			nums = append(nums, strconv.Itoa(dayNumber[d]))
		}
	}
	return strings.Join(nums, ","), nil
}
