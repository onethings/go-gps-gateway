package api

import (
	"context"
	"errors"
	"net/http"

	"traccar-server_go/go_server/internal/auth"
	"traccar-server_go/go_server/internal/ldap"
	"traccar-server_go/go_server/internal/model"
)

// LDAPAuthenticator is the directory lookup the login handler falls back to.
type LDAPAuthenticator interface {
	Authenticate(ctx context.Context, username, password string) (*ldap.Identity, error)
}

// WithLDAP enables directory authentication. A user that has no local password
// is checked against the directory, and an unknown address is looked up there
// before the login is refused.
func WithLDAP(authenticator LDAPAuthenticator) Option {
	return func(s *Server) { s.ldap = authenticator }
}

// ldapLogin authenticates against the directory, creates the local account on
// first sight and issues the same token a password login would.
func (s *Server) ldapLogin(
	ctx context.Context, w http.ResponseWriter, r *http.Request, email, password string,
) (*model.APIToken, string, bool) {
	identity, err := s.ldap.Authenticate(ctx, email, password)
	if err != nil {
		if errors.Is(err, ldap.ErrInvalidCredentials) {
			s.audit(r, model.ActionDenied, "", 0)
			writeError(w, http.StatusUnauthorized, errors.New("invalid email or password"))
			return nil, "", false
		}
		s.logf("api: ldap authenticate %q: %v", email, err)
		writeError(w, http.StatusBadGateway, errors.New("directory is unavailable"))
		return nil, "", false
	}

	user, err := s.ldapAccount(ctx, identity)
	if err != nil {
		s.logf("api: ldap account %q: %v", identity.Email, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return nil, "", false
	}
	if user.Disabled {
		s.audit(r, model.ActionDenied, "", 0)
		writeError(w, http.StatusForbidden, errors.New("account is disabled"))
		return nil, "", false
	}

	token, plaintext, err := s.users.IssueTokenForUser(ctx, user.ID, []string{string(auth.ScopeAll)}, nil)
	if err != nil {
		s.logf("api: issue ldap token for user %d: %v", user.ID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return nil, "", false
	}
	s.recordLogin(r, user.ID)
	return token, plaintext, true
}

// ldapAccount finds the local row of a directory identity, creating it the
// first time the directory confirms the user.
func (s *Server) ldapAccount(ctx context.Context, identity *ldap.Identity) (*model.User, error) {
	if s.users == nil {
		return nil, errors.New("users are not configured")
	}
	if identity.Email != "" {
		users, err := s.users.Users(ctx, model.UserFilter{Email: identity.Email, Limit: 1})
		if err != nil {
			return nil, err
		}
		if len(users) > 0 {
			return &users[0], nil
		}
	}
	name := identity.Name
	if name == "" {
		name = identity.Email
	}
	if name == "" {
		name = identity.DN
	}
	return s.users.CreateUser(ctx, model.UserCreate{Email: identity.Email, Name: name})
}
