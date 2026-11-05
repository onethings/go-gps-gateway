package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"traccar-server_go/go_server/internal/auth"
	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/permissions"
)

// ErrUnknownUser is returned when a token is issued for an email that has no
// users row.
var ErrUnknownUser = errors.New("storage: unknown user")

const (
	defaultUserLimit = 100
	maxUserLimit     = 1000

	// userSelectColumns feeds scanUser for the list and single row queries.
	// The two subqueries let an admin list accounts without one request per
	// user.
	userSelectColumns = `u.id, u.email, u.name, u.admin, u.disabled, u.temporary, u.created_at,
		(SELECT count(*) FROM permissions p
		  WHERE p.owner_type = 'user' AND p.owner_id = u.id AND p.property_type = 'device'),
		(SELECT count(*) FROM api_tokens t WHERE t.user_id = u.id),
		u.attributes, u.device_limit, u.user_limit`

	// tokenColumns is the SELECT list shared by every token query. scopes is a
	// JSON column, so every dialect returns the same text.
	tokenColumns = `id, user_id, scopes, expires_at, created_at`
)

// LookupToken resolves a hashed bearer token to its principal. A token that
// does not exist, expired, belongs to a deleted user, or belongs to a disabled
// user yields (nil, nil) so the HTTP layer can answer 401 without
// distinguishing the cases.
func (s *Store) LookupToken(ctx context.Context, tokenHash string) (*auth.Principal, error) {
	// The cache serves a resolved principal (or a known miss) without touching
	// the database. A nil cache is the disabled path; the generation passed to
	// get is only meaningful against the cache that owns it, so when there is no
	// cache the lookups always fall through.
	if cache := s.tokens; cache != nil {
		if principal, present, stale := cache.get(tokenHash, cache.generation()); present && !stale {
			return principal, nil
		}
	}

	var (
		tokenID int64
		userID  int64
		admin   bool
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT t.id, u.id, u.admin
		 FROM api_tokens t
		 JOIN users u ON u.id = t.user_id
		 WHERE t.token_hash = $1
		   AND NOT u.disabled
		   AND (t.expires_at IS NULL OR t.expires_at > $2)`,
		tokenHash, time.Now().UTC(),
	).Scan(&tokenID, &userID, &admin)
	if errors.Is(err, sql.ErrNoRows) {
		if cache := s.tokens; cache != nil {
			// Cache the miss so an unknown token does not hammer the database;
			// IssueToken bumps the generation, so a later issue is never hidden
			// by this entry.
			cache.put(tokenHash, cache.generation(), nil)
		}
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("storage: lookup token: %w", err)
	}

	var scopesJSON []byte
	if err := s.db.QueryRowContext(ctx,
		`SELECT scopes FROM api_tokens WHERE id = $1`, tokenID).Scan(&scopesJSON); err != nil {
		return nil, fmt.Errorf("storage: lookup token scopes: %w", err)
	}
	var rawScopes []string
	if err := json.Unmarshal(scopesJSON, &rawScopes); err != nil {
		return nil, fmt.Errorf("storage: decode token scopes: %w", err)
	}
	deviceIDs, err := s.permissionIDs(ctx, userID, "device")
	if err != nil {
		return nil, err
	}
	groupIDs, err := s.permissionIDs(ctx, userID, "group")
	if err != nil {
		return nil, err
	}

	// Group grants are expanded once per request instead of per query, which is
	// what the Java server does with Condition.Permission(includeGroups). An
	// admin reaches every device anyway, so the expansion is skipped.
	if !admin && len(groupIDs) > 0 {
		inherited, err := s.inheritedDeviceIDs(ctx, groupIDs)
		if err != nil {
			return nil, err
		}
		deviceIDs = append(deviceIDs, inherited...)
	}

	scopes := make([]auth.Scope, 0, len(rawScopes))
	for _, scope := range rawScopes {
		if scope != "" {
			scopes = append(scopes, auth.Scope(scope))
		}
	}
	principal := auth.NewPrincipal(userID, admin, scopes, deviceIDs)
	principal.TokenID = tokenID
	if cache := s.tokens; cache != nil {
		cache.put(tokenHash, cache.generation(), principal)
	}
	return principal, nil
}

// Ping reports whether the database is reachable, for the health probe.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("storage: ping: %w", err)
	}
	return nil
}

// Users returns every account, oldest first. Limit is clamped to 1..1000 and
// Offset to a non-negative value.
func (s *Store) Users(ctx context.Context, filter model.UserFilter) ([]model.User, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = defaultUserLimit
	}
	if limit > maxUserLimit {
		limit = maxUserLimit
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}

	query := "SELECT " + userSelectColumns + " FROM users u"
	args := make([]any, 0, 3)
	if filter.Email != "" {
		args = append(args, filter.Email)
		query += " WHERE u.email = $1"
	}
	args = append(args, limit, offset)
	query += fmt.Sprintf(" ORDER BY u.id LIMIT $%d OFFSET $%d", len(args)-1, len(args))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("storage: query users: %w", err)
	}
	defer rows.Close()

	var users []model.User
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, *user)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: query users: %w", err)
	}
	return users, nil
}

// User returns one account by id.
func (s *Store) User(ctx context.Context, userID int64) (*model.User, error) {
	user, err := scanUser(s.db.QueryRowContext(ctx,
		"SELECT "+userSelectColumns+" FROM users u WHERE u.id = $1", userID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("storage: query user %d: %w", userID, err)
	}
	return user, nil
}

// scanUser reads one row in userSelectColumns order.
func scanUser(row interface{ Scan(dest ...any) error }) (*model.User, error) {
	var (
		user       model.User
		attributes []byte
	)
	if err := row.Scan(
		&user.ID, &user.Email, &user.Name, &user.Admin, &user.Disabled, &user.Temporary,
		&user.CreatedAt, &user.DeviceCount, &user.TokenCount, &attributes,
		&user.DeviceLimit, &user.UserLimit,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// QueryRow hands sql.ErrNoRows straight to Scan; callers map it to
			// their own "not found" sentinel.
			return nil, err
		}
		return nil, fmt.Errorf("storage: scan user: %w", err)
	}
	decoded, err := decodeAttributes(attributes)
	if err != nil {
		return nil, fmt.Errorf("storage: user %d: %w", user.ID, err)
	}
	user.Attributes = decoded
	return &user, nil
}

// CredentialByEmail returns what a password login needs for an email. The
// match is exact: users.email is UNIQUE and case sensitive, so one address can
// only ever resolve to one account.
//
// An account without a password comes back with an empty PasswordHash, which
// auth.VerifyPassword always rejects.
func (s *Store) CredentialByEmail(ctx context.Context, email string) (*auth.Credential, error) {
	return s.credential(ctx, `WHERE email = $1`, email)
}

// CredentialByID returns the password state of one account. A self-service
// password change knows the caller's id, not its email.
func (s *Store) CredentialByID(ctx context.Context, userID int64) (*auth.Credential, error) {
	return s.credential(ctx, `WHERE id = $1`, userID)
}

func (s *Store) credential(ctx context.Context, predicate string, arg any) (*auth.Credential, error) {
	var (
		credential  auth.Credential
		lockedUntil sql.NullTime
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, COALESCE(password_hash, ''), disabled, locked_until,
		        COALESCE(totp_key, ''), totp_enabled
		   FROM users `+predicate,
		arg,
	).Scan(&credential.UserID, &credential.PasswordHash, &credential.Disabled, &lockedUntil,
		&credential.TOTPSecret, &credential.TOTPEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("storage: credential: %w", err)
	}
	if lockedUntil.Valid {
		lock := lockedUntil.Time
		credential.LockedUntil = &lock
	}
	return &credential, nil
}

// UserTOTP returns the shared secret and whether a code is currently required.
func (s *Store) UserTOTP(ctx context.Context, userID int64) (string, bool, error) {
	var (
		secret  string
		enabled bool
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(totp_key, ''), totp_enabled FROM users WHERE id = $1`,
		userID,
	).Scan(&secret, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, model.ErrUserNotFound
	}
	if err != nil {
		return "", false, fmt.Errorf("storage: user totp: %w", err)
	}
	return secret, enabled, nil
}

// SetUserTOTP stores the shared secret and the enabled flag. An empty secret
// disables two factor authentication for the account.
func (s *Store) SetUserTOTP(ctx context.Context, userID int64, secret string, enabled bool) error {
	var key *string
	if secret != "" {
		key = &secret
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE users SET totp_key = $2, totp_enabled = $3 WHERE id = $1`,
		userID, key, enabled && secret != "")
	if err != nil {
		return fmt.Errorf("storage: set user totp: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("storage: set user totp: %w", err)
	}
	if affected == 0 {
		return model.ErrUserNotFound
	}
	s.invalidateTokenCache()
	return nil
}

// RecordLoginFailure counts one wrong password and locks the account once the
// threshold is reached. It returns the lock deadline, or nil while the account
// stays open.
//
// The row is locked for the read-modify-write so that parallel guesses cannot
// each read the same counter and lose an increment, which would let an attacker
// sidestep the threshold by flooding. An expired lock does not count as an
// attempt, so the window starts over instead of re-locking on the first new
// failure.
func (s *Store) RecordLoginFailure(ctx context.Context, userID int64, threshold int, lockFor time.Duration) (*time.Time, error) {
	if threshold <= 0 {
		return nil, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("storage: record login failure of user %d: %w", userID, err)
	}
	defer tx.Rollback()

	var (
		attempts    int
		lockedUntil sql.NullTime
	)
	selectQuery := `SELECT failed_login_attempts, locked_until FROM users WHERE id = $1`
	if s.db.dialect.name != "sqlite" {
		selectQuery += " FOR UPDATE"
	}
	err = tx.QueryRowContext(ctx, selectQuery, userID).Scan(&attempts, &lockedUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("storage: record login failure of user %d: %w", userID, err)
	}

	now := time.Now().UTC()
	if lockedUntil.Valid && !lockedUntil.Time.After(now) {
		attempts = 0
	}
	attempts++

	var lock *time.Time
	if attempts >= threshold {
		deadline := now.Add(lockFor)
		lock = &deadline
		lockedUntil = sql.NullTime{Time: deadline, Valid: true}
	} else {
		lockedUntil = sql.NullTime{}
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE users SET failed_login_attempts = $2, locked_until = $3 WHERE id = $1`,
		userID, attempts, lockedUntil,
	); err != nil {
		return nil, fmt.Errorf("storage: record login failure of user %d: %w", userID, err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("storage: record login failure of user %d: %w", userID, err)
	}
	return lock, nil
}

// ResetLoginFailures clears the counters after a successful password check or
// a password change.
func (s *Store) ResetLoginFailures(ctx context.Context, userID int64) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE users SET failed_login_attempts = 0, locked_until = NULL WHERE id = $1`, userID)
	if err != nil {
		return fmt.Errorf("storage: reset login failures of user %d: %w", userID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("storage: reset login failures of user %d: %w", userID, err)
	}
	if affected == 0 {
		return model.ErrUserNotFound
	}
	return nil
}

// CreateUser inserts an account. An email that already exists returns
// model.ErrDuplicateUser instead of touching the row.
func (s *Store) CreateUser(ctx context.Context, create model.UserCreate) (*model.User, error) {
	attributes, err := encodeAttributes(create.Attributes)
	if err != nil {
		return nil, err
	}

	userID, err := insertReturningID(ctx, s.db,
		`INSERT INTO users (email, name, admin, temporary, attributes, device_limit, user_limit)
		 VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		create.Email, create.Name, create.Admin, create.Temporary, attributes,
		limitOrUnbounded(create.DeviceLimit), limitOrUnbounded(create.UserLimit))
	if err != nil {
		if s.db.dialect.isDuplicate(err) {
			return nil, model.ErrDuplicateUser
		}
		return nil, fmt.Errorf("storage: create user %q: %w", create.Email, err)
	}
	s.invalidateTokenCache()
	return s.User(ctx, userID)
}

// limitOrUnbounded resolves an unset limit, so an account created without one
// can hold as many devices as before the columns existed.
func limitOrUnbounded(limit *int) int {
	if limit == nil {
		return model.NoLimit
	}
	return *limit
}

// UpdateUser replaces the non-nil fields of an account.
func (s *Store) UpdateUser(ctx context.Context, userID int64, update model.UserUpdate) (*model.User, error) {
	var attributes any
	if update.Attributes != nil {
		encoded, err := encodeAttributes(*update.Attributes)
		if err != nil {
			return nil, err
		}
		attributes = encoded
	}

	query := `UPDATE users
		 SET name = COALESCE($2, name),
		     admin = COALESCE($3, admin),
		     disabled = COALESCE($4, disabled),
		     device_limit = COALESCE($5, device_limit),
		     user_limit = COALESCE($6, user_limit)`
	args := []any{userID, update.Name, update.Admin, update.Disabled,
		update.DeviceLimit, update.UserLimit}
	if attributes != nil {
		query += ", attributes = $7"
		args = append(args, attributes)
	}
	query += " WHERE id = $1"

	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("storage: update user %d: %w", userID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("storage: update user %d: %w", userID, err)
	}
	if affected == 0 {
		return nil, model.ErrUserNotFound
	}
	s.invalidateTokenCache()
	return s.User(ctx, userID)
}

// SetPassword replaces the stored bcrypt hash of an account. It is a separate
// statement from UpdateUser because a password arrives hashed, not as a model
// field the REST layer could echo back.
func (s *Store) SetPassword(ctx context.Context, userID int64, passwordHash string) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE users SET password_hash = $2 WHERE id = $1`, userID, passwordHash)
	if err != nil {
		return fmt.Errorf("storage: set password of user %d: %w", userID, err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("storage: set password of user %d: %w", userID, err)
	}
	if affected == 0 {
		return model.ErrUserNotFound
	}
	return nil
}

// DeleteUser removes an account. Grants and tokens cascade.
func (s *Store) DeleteUser(ctx context.Context, userID int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, userID)
	if err != nil {
		return fmt.Errorf("storage: delete user %d: %w", userID, err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("storage: delete user %d: %w", userID, err)
	}
	if affected == 0 {
		return model.ErrUserNotFound
	}

	// permissions has no foreign key to users because a pair may name any
	// object type, so the grants of the account are dropped here.
	if err := s.ClearPermissions(ctx, permissions.ObjectUser, userID); err != nil {
		return err
	}
	// The tokens went away with the row, so a principal cached for one of them
	// must not outlive the account.
	s.invalidateTokenCache()
	return nil
}

// UserDevices returns the device ids granted to a user, in id order.
func (s *Store) UserDevices(ctx context.Context, userID int64) ([]int64, error) {
	return s.permissionIDs(ctx, userID, "device")
}

// permissionIDs reads the property ids of one grant type. It is the portable
// replacement for the jsonb_agg subqueries the PostgreSQL-only version used.
func (s *Store) permissionIDs(ctx context.Context, userID int64, propertyType string) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT property_id FROM permissions
		  WHERE owner_type = 'user' AND owner_id = $1 AND property_type = $2
		  ORDER BY property_id`,
		userID, propertyType)
	if err != nil {
		return nil, fmt.Errorf("storage: query user %d %s permissions: %w", userID, propertyType, err)
	}
	defer rows.Close()

	ids := make([]int64, 0, 4)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("storage: scan user %d %s permission: %w", userID, propertyType, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: query user %d %s permissions: %w", userID, propertyType, err)
	}
	return ids, nil
}

// AssignDevice grants a user access to a device. Repeating the call is a no-op.
func (s *Store) AssignDevice(ctx context.Context, userID, deviceID int64) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO permissions (owner_type, owner_id, property_type, property_id)
		 VALUES ('user', $1, 'device', $2)`, userID, deviceID)
	if err != nil && !s.db.dialect.isDuplicate(err) {
		return fmt.Errorf("storage: assign device %d to user %d: %w", deviceID, userID, err)
	}
	s.invalidateTokenCache()
	return nil
}

// UnassignDevice revokes a grant. Removing a grant that does not exist is not
// an error, so a repeated DELETE stays idempotent.
func (s *Store) UnassignDevice(ctx context.Context, userID, deviceID int64) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM permissions
		  WHERE owner_type = 'user' AND owner_id = $1
		    AND property_type = 'device' AND property_id = $2`,
		userID, deviceID)
	if err != nil {
		return fmt.Errorf("storage: unassign device %d from user %d: %w", deviceID, userID, err)
	}
	s.invalidateTokenCache()
	return nil
}

// Tokens lists the tokens of a user, newest first. The hash is never returned.
func (s *Store) Tokens(ctx context.Context, userID int64) ([]model.APIToken, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+tokenColumns+" FROM api_tokens WHERE user_id = $1 ORDER BY id DESC",
		userID)
	if err != nil {
		return nil, fmt.Errorf("storage: query user %d tokens: %w", userID, err)
	}
	defer rows.Close()

	var tokens []model.APIToken
	for rows.Next() {
		token, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, *token)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: query user %d tokens: %w", userID, err)
	}
	return tokens, nil
}

// IssueToken creates a bearer token for an existing user and returns the
// plaintext token. Only its hash is stored. A nil expiresAt means the token
// never expires.
func (s *Store) IssueToken(ctx context.Context, email string, scopes []auth.Scope, expiresAt *time.Time) (string, error) {
	names := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		names = append(names, string(scope))
	}

	_, token, err := s.issueToken(ctx, "u.email = $1", email, names, expiresAt, ErrUnknownUser)
	return token, err
}

// IssueTokenForUser creates a bearer token for a user id. The returned record
// is the stored row; the plaintext token is only available from this call.
func (s *Store) IssueTokenForUser(ctx context.Context, userID int64, scopes []string, expiresAt *time.Time) (*model.APIToken, string, error) {
	return s.issueToken(ctx, "u.id = $1", userID, scopes, expiresAt, model.ErrUserNotFound)
}

// issueToken inserts a token for the user selected by predicate. Selecting the
// user in the same statement keeps "no such user" distinguishable from a real
// failure without a second round trip.
func (s *Store) issueToken(ctx context.Context, predicate string, userArg any, scopes []string, expiresAt *time.Time, notFound error) (*model.APIToken, string, error) {
	token, err := auth.GenerateToken()
	if err != nil {
		return nil, "", err
	}

	if scopes == nil {
		// json.Marshal(nil slice) emits "null", which is not a JSON array and
		// would make jsonb_array_elements_text fail.
		scopes = []string{}
	}
	encoded, err := json.Marshal(scopes)
	if err != nil {
		return nil, "", fmt.Errorf("storage: encode token scopes: %w", err)
	}

	var userID int64
	err = s.db.QueryRowContext(ctx,
		`SELECT u.id FROM users u WHERE `+predicate, userArg).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", notFound
	}
	if err != nil {
		return nil, "", fmt.Errorf("storage: issue token: %w", err)
	}

	tokenID, err := insertReturningID(ctx, s.db,
		`INSERT INTO api_tokens (user_id, token_hash, scopes, expires_at)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		userID, auth.HashToken(token), string(encoded), expiresAt)
	if err != nil {
		return nil, "", fmt.Errorf("storage: issue token: %w", err)
	}

	record, err := scanToken(s.db.QueryRowContext(ctx,
		"SELECT "+tokenColumns+" FROM api_tokens WHERE id = $1", tokenID))
	if err != nil {
		return nil, "", fmt.Errorf("storage: issue token: %w", err)
	}
	s.invalidateTokenCache()
	return record, token, nil
}

// RevokeToken deletes one token of one user. A token that belongs to another
// user is reported as missing instead of being deleted.
func (s *Store) RevokeToken(ctx context.Context, userID, tokenID int64) error {
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM api_tokens WHERE id = $1 AND user_id = $2`, tokenID, userID)
	if err != nil {
		return fmt.Errorf("storage: revoke token %d: %w", tokenID, err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("storage: revoke token %d: %w", tokenID, err)
	}
	if affected == 0 {
		return model.ErrTokenNotFound
	}
	s.invalidateTokenCache()
	return nil
}

// RevokeTokensExcept deletes every token of one user except keepTokenID. A
// keepTokenID of zero removes all of them, which is what a password reset
// needs: the caller holds a reset code, not a token.
func (s *Store) RevokeTokensExcept(ctx context.Context, userID, keepTokenID int64) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM api_tokens WHERE user_id = $1 AND id <> $2`, userID, keepTokenID)
	if err != nil {
		return fmt.Errorf("storage: revoke tokens of user %d: %w", userID, err)
	}
	s.invalidateTokenCache()
	return nil
}

// scanToken reads one row in tokenColumns order.
func scanToken(row interface{ Scan(dest ...any) error }) (*model.APIToken, error) {
	var (
		token      model.APIToken
		scopesJSON []byte
		expiresAt  sql.NullTime
	)
	if err := row.Scan(&token.ID, &token.UserID, &scopesJSON, &expiresAt, &token.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("storage: scan token: %w", err)
	}

	if err := json.Unmarshal(scopesJSON, &token.Scopes); err != nil {
		return nil, fmt.Errorf("storage: decode token scopes: %w", err)
	}
	if token.Scopes == nil {
		token.Scopes = []string{}
	}
	if expiresAt.Valid {
		expiry := expiresAt.Time
		token.ExpiresAt = &expiry
	}
	return &token, nil
}
