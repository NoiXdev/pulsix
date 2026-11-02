package setup

// SetPasswordHasher replaces the argon2id hash Complete runs, so a test can
// see whether it ran at all.
func (s *Service) SetPasswordHasher(hash func(string) (string, error)) { s.hashPassword = hash }
