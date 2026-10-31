package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"traccar-server_go/go_server/internal/auth"
)

// ErrUnknownUser is returned when a token is issued for an email that has no
// users row.
var ErrUnknownUser = errors.New("postgres: unknown user")

// LookupToken resolves a hashed bearer token to its principal. A token that
// does not exist, expired, or belongs to a deleted user yields (nil, nil) so
// the HTTP layer can answer 401 without distinguishing the cases.
func (s *Store) LookupToken(ctx context.Context, tokenHash string) (*auth.Principal, error) {
	var (
		userID      int64
		admin       bool
		scopesJSON  string
		devicesJSON string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT u.id, u.admin,
		        to_jsonb(t.scopes)::text,
		        COALESCE((SELECT jsonb_agg(ud.device_id)
		                  FROM user_devices ud WHERE ud.user_id = u.id), '[]'::jsonb)::text
		 FROM api_tokens t
		 JOIN users u ON u.id = t.user_id
		 WHERE t.token_hash = $1
		   AND (t.expires_at IS NULL OR t.expires_at > now())`,
		tokenHash,
	).Scan(&userID, &admin, &scopesJSON, &devicesJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: lookup token: %w", err)
	}

	var rawScopes []string
	if err := json.Unmarshal([]byte(scopesJSON), &rawScopes); err != nil {
		return nil, fmt.Errorf("postgres: decode token scopes: %w", err)
	}
	var deviceIDs []int64
	if err := json.Unmarshal([]byte(devicesJSON), &deviceIDs); err != nil {
		return nil, fmt.Errorf("postgres: decode token devices: %w", err)
	}

	scopes := make([]auth.Scope, 0, len(rawScopes))
	for _, scope := range rawScopes {
		if scope != "" {
			scopes = append(scopes, auth.Scope(scope))
		}
	}
	return auth.NewPrincipal(userID, admin, scopes, deviceIDs), nil
}

// AssignDevice grants a user access to a device. Repeating the call is a no-op.
func (s *Store) AssignDevice(ctx context.Context, userID, deviceID int64) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO user_devices (user_id, device_id) VALUES ($1, $2)
		 ON CONFLICT DO NOTHING`, userID, deviceID)
	if err != nil {
		return fmt.Errorf("postgres: assign device %d to user %d: %w", deviceID, userID, err)
	}
	return nil
}

// IssueToken creates a bearer token for an existing user and returns the
// plaintext token. Only its hash is stored. A nil expiresAt means the token
// never expires.
func (s *Store) IssueToken(ctx context.Context, email string, scopes []auth.Scope, expiresAt *time.Time) (string, error) {
	token, err := auth.GenerateToken()
	if err != nil {
		return "", err
	}

	if scopes == nil {
		// json.Marshal(nil slice) emits "null", which is not a JSON array and
		// would make jsonb_array_elements_text fail.
		scopes = []auth.Scope{}
	}
	encoded, err := json.Marshal(scopes)
	if err != nil {
		return "", fmt.Errorf("postgres: encode token scopes: %w", err)
	}

	result, err := s.db.ExecContext(ctx,
		`INSERT INTO api_tokens (user_id, token_hash, scopes, expires_at)
		 SELECT id, $2, ARRAY(SELECT jsonb_array_elements_text($3::jsonb)), $4
		 FROM users WHERE email = $1`,
		email, auth.HashToken(token), string(encoded), expiresAt)
	if err != nil {
		return "", fmt.Errorf("postgres: issue token for %q: %w", email, err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return "", fmt.Errorf("postgres: issue token for %q: %w", email, err)
	}
	if affected == 0 {
		return "", ErrUnknownUser
	}
	return token, nil
}
