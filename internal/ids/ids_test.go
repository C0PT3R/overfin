package ids

import "testing"

func TestNewAndValid(t *testing.T) {
	id := New(Profile)
	if !Valid(Profile, id) {
		t.Fatalf("%q is not valid", id)
	}
	if Valid(Account, id) {
		t.Error("a profile ID passed as an account ID")
	}
	if New(Profile) == id {
		t.Error("two IDs are equal")
	}
	for _, bad := range []string{"", "prof_", "prof_ABC", "prof_" + id[5:] + "a", "prof-" + id[5:], "prof_../../etc"} {
		if Valid(Profile, bad) {
			t.Errorf("%q passed as valid", bad)
		}
	}
}
