package monitorsettings

// The retention choice (backlog 3.15): an organization keeps its
// measurements and state history (check_samples, check_state_changes) for
// 30, 90 or 365 days, or makes no choice and keeps them as long as the
// instance maximum allows. The table's CHECK (00093) admits the same
// three values.

// ValidRetentionDays reports whether days is one of the retention choices
// an organization can store: 30, 90 or 365.
func ValidRetentionDays(days int) bool {
	switch days {
	case 30, 90, 365:
		return true
	}
	return false
}

// EffectiveRetentionDays is how many days an organization keeps its
// measurements: its choice when that is below the instance maximum, else
// the maximum (choice 0 = no choice). The settings API shows it and the
// Purger deletes by it; ListOrgsWithShorterRetention selects exactly the
// organizations for which it is below the maximum.
func EffectiveRetentionDays(choice, maxDays int) int {
	if choice > 0 && choice < maxDays {
		return choice
	}
	return maxDays
}
