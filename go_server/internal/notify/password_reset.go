package notify

import (
	"context"
	"fmt"
	"time"
)

// PasswordResetMail sends a password reset code through the SMTP settings that
// the alert mail channel already uses. It is not a Notifier: the message is not
// an alert and it always has exactly one recipient.
type PasswordResetMail struct {
	mail *Mail
}

// NewPasswordResetMail wraps the configured SMTP channel.
func NewPasswordResetMail(mail *Mail) *PasswordResetMail {
	return &PasswordResetMail{mail: mail}
}

// SendPasswordReset delivers one code to the account address.
func (m *PasswordResetMail) SendPasswordReset(ctx context.Context, to, token string, ttl time.Duration) error {
	return m.mail.Send(ctx, PasswordResetMessage(token, ttl), []string{to})
}

// PasswordResetMessage renders the reset email. The code is the only secret in
// it; the surrounding text says what it is for and when it stops working.
func PasswordResetMessage(token string, ttl time.Duration) Message {
	minutes := int(ttl / time.Minute)
	if minutes < 1 {
		minutes = 1
	}
	body := fmt.Sprintf(`Someone asked to reset the password of this traccar-server-go account.

Enter the code below to choose a new password. It works once and expires in %d minutes.

%s

If you did not ask for a reset, ignore this message; the current password keeps working.
`, minutes, token)
	return Message{Subject: "traccar-server-go password reset", Body: body}
}
