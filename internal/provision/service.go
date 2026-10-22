package provision

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/store"
)

// serviceDevice is the service account's one device ID: it signs in once,
// and signing in again on it would cancel its token (Jellyfin 12).
const serviceDevice = "overfin-service"

// Service is Overfin's Jellyfin service account: an ordinary user of its
// own (never an administrator, hidden, with every library) that reads the
// whole library for the catalog. Creating it is provisioning: the admin key
// is used for that, and to give it a new token if Jellyfin refuses the old
// one (SECURITY.md rule 3); everything else runs with its own token.
type Service struct {
	// Provisioner is the current one: its admin key follows the
	// configuration.
	Provisioner func() *Provisioner

	mu      sync.Mutex
	account *store.ServiceAccount
}

// Auth returns the service account's credentials, making the account on
// first use.
func (s *Service) Auth(ctx context.Context) (jellyfin.Auth, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.account == nil {
		a, err := s.Provisioner().Store.Service(ctx)
		switch {
		case errors.Is(err, store.ErrServiceToken):
			// Sealed with a lost secret key: the account stays, with a new
			// token.
			s.account = a
			if err := s.renew(ctx); err != nil {
				s.account = nil // tried again next time
				return jellyfin.Auth{}, "", err
			}
		case err != nil:
			return jellyfin.Auth{}, "", err
		case a == nil:
			if a, err = s.create(ctx); err != nil {
				return jellyfin.Auth{}, "", err
			}
			s.account = a
		default:
			s.account = a
		}
	}
	return jellyfin.Auth{Token: s.account.Token, DeviceID: serviceDevice}, s.account.UserID, nil
}

// Refused tells the service that Jellyfin refused its token: it gets a new
// one, or, if the account itself is gone, a new account.
func (s *Service) Refused(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.account == nil {
		return nil // nothing was handed out yet
	}
	return s.renew(ctx)
}

// renew gives the service account a new token, or a new account if it's
// gone. The caller holds s.mu.
func (s *Service) renew(ctx context.Context) error {
	p := s.Provisioner()
	admin := jellyfin.Auth{Token: p.AdminKey, DeviceID: "overfin-admin"}
	password := randomSecret()
	err := p.Jellyfin.SetPassword(ctx, admin, s.account.UserID, password)
	if jellyfin.IsStatus(err, http.StatusNotFound) {
		p.Logger.Warn("the Jellyfin service account is gone; making a new one")
		a, err := s.create(ctx)
		if err == nil {
			s.account = a
		}
		return err
	}
	if err != nil {
		return err
	}
	token, _, err := p.Jellyfin.Authenticate(ctx, serviceDevice, s.account.Username, password)
	if err != nil {
		return err
	}
	a := *s.account
	a.Token = token
	if err := p.Store.SetService(ctx, a); err != nil {
		return err
	}
	s.account = &a
	p.Logger.Info("the Jellyfin service account has a new token")
	return nil
}

// create makes the service account: hidden, never an administrator, every
// library, no playback yet (it only reads). Its password is used once, to
// sign in, and never kept.
func (s *Service) create(ctx context.Context) (*store.ServiceAccount, error) {
	p := s.Provisioner()
	admin := jellyfin.Auth{Token: p.AdminKey, DeviceID: "overfin-admin"}
	username := "overfin-service-" + randomName()[:6]
	password := randomSecret()
	userID, err := p.Jellyfin.CreateUser(ctx, admin, username, password)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			p.rollback(userID)
		}
	}()
	policy, err := p.Jellyfin.UserPolicy(ctx, admin, userID)
	if err != nil {
		return nil, err
	}
	policy.LockDown()
	policy["EnableAllFolders"] = true
	policy["EnabledFolders"] = []string{}
	policy["EnableMediaPlayback"] = false
	policy["EnableAudioPlaybackTranscoding"] = false
	policy["EnableVideoPlaybackTranscoding"] = false
	policy["EnablePlaybackRemuxing"] = false
	if err := p.Jellyfin.SetUserPolicy(ctx, admin, userID, policy); err != nil {
		return nil, err
	}
	token, authID, err := p.Jellyfin.Authenticate(ctx, serviceDevice, username, password)
	if err != nil {
		return nil, err
	}
	if authID != userID {
		return nil, errors.New("provision: signed in as another user than the service account")
	}
	a := &store.ServiceAccount{UserID: userID, Username: username, Token: token}
	if err := p.Store.SetService(ctx, *a); err != nil {
		return nil, err
	}
	ok = true
	p.Logger.Info("made the Jellyfin service account", "user", username)
	return a, nil
}
