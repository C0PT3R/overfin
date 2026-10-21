package provision

import (
	"context"
	"strings"
	"testing"
)

func TestServiceAccount(t *testing.T) {
	fake := &fakeJellyfin{}
	p := setup(t, fake, "")
	s := &Service{Provisioner: func() *Provisioner { return p }}
	ctx := context.Background()

	a, userID, err := s.Auth(ctx)
	if err != nil || userID != jfUser || a.Token != "token-"+jfUser || a.DeviceID != serviceDevice {
		t.Fatalf("first use: %+v %s %v", a, userID, err)
	}
	if len(fake.created) != 1 || !strings.HasPrefix(fake.created[0], "overfin-service-") {
		t.Errorf("created: %v", fake.created)
	}
	// Never an administrator, hidden, every library, no playback.
	for k, want := range map[string]any{"IsAdministrator": false, "IsHidden": true, "EnableAllFolders": true,
		"EnableMediaPlayback": false, "EnableVideoPlaybackTranscoding": false} {
		if fake.policy[k] != want {
			t.Errorf("policy %s = %v, want %v", k, fake.policy[k], want)
		}
	}

	// Made once: a new start finds it in the database, its token sealed.
	again := &Service{Provisioner: func() *Provisioner { return p }}
	if a2, _, err := again.Auth(ctx); err != nil || a2.Token != a.Token || len(fake.created) != 1 {
		t.Errorf("second start: %+v %v, created %v", a2, err, fake.created)
	}

	// A refused token: a new password, a new sign-in, on the same device.
	logins := len(fake.logins)
	if err := again.Refused(ctx); err != nil || fake.passwords != 1 || len(fake.logins) != logins+1 {
		t.Errorf("renewed: %v, passwords %d, logins %d", err, fake.passwords, len(fake.logins)-logins)
	}

	// The account deleted in Jellyfin: a new one.
	fake.serviceGone = true
	if err := again.Refused(ctx); err != nil || len(fake.created) != 2 {
		t.Errorf("gone: %v, created %v", err, fake.created)
	}
}
