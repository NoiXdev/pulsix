package accounts

// WaitForMail waits until every reset mail RequestReset sent in the
// background has been handed to the mailer (or failed).
func WaitForMail(s *Service) { s.mails.Wait() }
