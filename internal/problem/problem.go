// Package problem writes error responses as RFC 9457 problem details.
//
// Every error type Overfin can return is listed here, with its HTTP status
// and title, so the API's errors stay consistent and documented in one place.
// Clients branch on Type; Detail is for humans only.
package problem

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// Type is a stable, machine-readable error name.
type Type string

const (
	NotFound         Type = "not-found"
	NotAuthenticated Type = "not-authenticated"
	OwnerOnly        Type = "owner-only"
	// AccountNotAllowed: the identity may not use this instance (not in
	// the required group, or its account is disabled).
	AccountNotAllowed Type = "account-not-allowed"
	// AccountSetupRequired: no account yet; the site offers to start fresh
	// or to claim an existing media server account.
	AccountSetupRequired Type = "account-setup-required"
	// InvitationRequired: accounts are created by invitation only here, and
	// the visitor has no valid one.
	InvitationRequired Type = "invitation-required"
	// AccountExpired: the operator set an end to this account's access.
	AccountExpired Type = "account-expired"
	AccountExists  Type = "account-exists"
	// ClaimFailed: wrong username or password, deliberately not saying
	// which. ClaimNotAllowed: the credentials were right, but that account
	// can't be adopted (an administrator, or already adopted).
	ClaimFailed         Type = "claim-failed"
	ClaimNotAllowed     Type = "claim-not-allowed"
	CrossSiteRequest    Type = "cross-site-request"
	NotPlayable         Type = "not-playable"
	StreamLimitReached  Type = "stream-limit-reached"
	NameTaken           Type = "name-taken"
	ProfileLimitReached Type = "profile-limit-reached"
	ServerBusy          Type = "server-busy"
	// A playback that no longer exists, and why.
	PlaybackTakenOver    Type = "playback-taken-over"
	PlaybackExpired      Type = "playback-expired"
	PlaybackStopped      Type = "playback-stopped"
	UnsupportedMediaType Type = "unsupported-media-type"
	InvalidRequest       Type = "invalid-request"
	InvalidCursor        Type = "invalid-cursor"
	RateLimited          Type = "rate-limited"
	// Requests: already asked for (or there), the quota used up, refused
	// by Seerr, and no longer pending (to cancel).
	AlreadyRequested Type = "already-requested"
	QuotaExceeded    Type = "quota-exceeded"
	RequestRefused   Type = "request-refused"
	NotPending       Type = "not-pending"
	// PINRequired: the profile has a PIN and none was given. PINIncorrect:
	// a wrong PIN. PINLocked: too many wrong PINs; retryAfter says when
	// another attempt is possible.
	PINRequired  Type = "pin-required"
	PINIncorrect Type = "pin-incorrect"
	PINLocked    Type = "pin-locked"
	// ProfileRequired: this session has no profile chosen yet; the site
	// shows the profile list ("Who's watching?").
	ProfileRequired Type = "profile-required"
	// StepUpRequired: the action needs a fresh login with the identity
	// provider.
	StepUpRequired Type = "step-up-required"
	Internal       Type = "internal-error"
)

type info struct {
	status int
	title  string
}

// registry holds the status and title of every type. The title is the same
// for every occurrence of a type.
var registry = map[Type]info{
	NotFound:             {http.StatusNotFound, "Not found"},
	NotAuthenticated:     {http.StatusUnauthorized, "Not logged in"},
	OwnerOnly:            {http.StatusForbidden, "Only the account owner can do this"},
	AccountNotAllowed:    {http.StatusForbidden, "This account may not use this service"},
	AccountSetupRequired: {http.StatusNotFound, "No account yet"},
	AccountExists:        {http.StatusConflict, "This account already exists"},
	AccountExpired:       {http.StatusForbidden, "Access to this account has ended"},
	InvitationRequired:   {http.StatusForbidden, "An invitation is needed to join"},
	ClaimFailed:          {http.StatusForbidden, "Wrong username or password"},
	ClaimNotAllowed:      {http.StatusForbidden, "This account can't be brought over"},
	CrossSiteRequest:     {http.StatusForbidden, "Cross-site requests are not allowed"},
	NotPlayable:          {http.StatusConflict, "This item can't be played"},
	StreamLimitReached:   {http.StatusConflict, "Too many streams on this account"},
	NameTaken:            {http.StatusConflict, "This name is already used in this account"},
	ProfileLimitReached:  {http.StatusConflict, "This account has as many profiles as allowed"},
	ServerBusy:           {http.StatusServiceUnavailable, "The server is busy; try again shortly"},
	PlaybackTakenOver:    {http.StatusGone, "Playback continued on another device"},
	PlaybackExpired:      {http.StatusGone, "Playback ended after a period of inactivity"},
	PlaybackStopped:      {http.StatusGone, "Playback was stopped"},
	UnsupportedMediaType: {http.StatusUnsupportedMediaType, "Requests must be JSON"},
	InvalidRequest:       {http.StatusBadRequest, "Invalid request"},
	InvalidCursor:        {http.StatusBadRequest, "Invalid cursor"},
	RateLimited:          {http.StatusTooManyRequests, "Too many requests"},
	AlreadyRequested:     {http.StatusConflict, "This is already requested or available"},
	QuotaExceeded:        {http.StatusConflict, "No requests left for now"},
	RequestRefused:       {http.StatusConflict, "This request was refused"},
	NotPending:           {http.StatusConflict, "This request is no longer pending"},
	PINRequired:          {http.StatusForbidden, "This profile is protected by a PIN"},
	PINIncorrect:         {http.StatusForbidden, "Wrong PIN"},
	PINLocked:            {http.StatusForbidden, "Too many wrong PINs; try again later"},
	StepUpRequired:       {http.StatusForbidden, "Please log in again to do this"},
	ProfileRequired:      {http.StatusForbidden, "Choose a profile first"},
	Internal:             {http.StatusInternalServerError, "Internal error"},
}

// Problem is one error response, with the extra fields some types carry.
type Problem struct {
	Type   Type   `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
	// TakenOverBy names the device that took over a playback.
	TakenOverBy string `json:"takenOverBy,omitempty"`
	// RetryAfter is how many seconds to wait before trying again; it is
	// also sent as the Retry-After header.
	RetryAfter int `json:"retryAfter,omitempty"`
	// StepUp and StepUpURL, for step-up-required: what confirms the action
	// ("login"), and where to send the user for it. StepUpURL is empty when
	// the instance offers no step-up.
	StepUp    string `json:"stepUp,omitempty"`
	StepUpURL string `json:"stepUpUrl,omitempty"`
	// Offers, for account-setup-required, are the other ways modules offer
	// to set an account up, each a member of its own: "claim": "move", say.
	Offers map[string]string `json:"-"`
}

// Members are the names a problem's own members use, which offers can't.
var Members = []string{"type", "title", "status", "detail", "takenOverBy", "retryAfter", "stepUp", "stepUpUrl"}

// New returns the problem for t, with an optional human-readable detail.
// The detail must never contain upstream error text or internal details
// (SECURITY.md rule 31).
func New(t Type, detail string) Problem {
	i, ok := registry[t]
	if !ok {
		// An unregistered type is a programming error; never leak it.
		t, detail = Internal, ""
		i = registry[Internal]
	}
	return Problem{Type: t, Title: i.title, Status: i.status, Detail: detail}
}

// Write sends p as the response.
func Write(w http.ResponseWriter, p Problem) {
	w.Header().Set("Content-Type", "application/problem+json")
	if p.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(p.RetryAfter))
	}
	w.WriteHeader(p.Status)
	if len(p.Offers) == 0 {
		_ = json.NewEncoder(w).Encode(p)
		return
	}
	b, _ := json.Marshal(p)
	var members map[string]any
	_ = json.Unmarshal(b, &members)
	for name, value := range p.Offers {
		if _, taken := members[name]; !taken {
			members[name] = value
		}
	}
	_ = json.NewEncoder(w).Encode(members)
}

// Send is New followed by Write.
func Send(w http.ResponseWriter, t Type, detail string) {
	Write(w, New(t, detail))
}
