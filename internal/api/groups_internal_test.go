package api

import (
	"fmt"
	"testing"
)

// A login keeps every group for a required group, else the admins' group
// alone; without either, it asks for none.
func TestKeptGroups(t *testing.T) {
	admins := &Auth{AdminGroup: "site-admins"}
	if !admins.asksGroups() || (&Auth{}).asksGroups() {
		t.Error("asksGroups")
	}
	for _, c := range []struct {
		auth *Auth
		raw  []string
		want string
	}{
		{admins, []string{"friends", "site-admins"}, "[site-admins]"},
		{admins, []string{"friends"}, "[]"},
		{&Auth{NeedGroups: true, AdminGroup: "site-admins"}, []string{"friends", "site-admins"}, "[friends site-admins]"},
	} {
		if got := fmt.Sprint(c.auth.keptGroups(c.raw)); got != c.want {
			t.Errorf("%v: kept %s, want %s", c.raw, got, c.want)
		}
	}
}
