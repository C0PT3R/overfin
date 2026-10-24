package proof

import (
	"strconv"
	"testing"
)

func solve(challenge string) string {
	for n := 0; ; n++ {
		if a := strconv.Itoa(n); Solves(challenge, a) {
			return a
		}
	}
}

func TestCheck(t *testing.T) {
	c := &Checker{Key: []byte("key")}
	ch := c.Challenge()
	a := solve(ch)
	if c.Check(ch, "x") {
		t.Error("a wrong answer")
	}
	if !c.Check(ch, a) {
		t.Fatal("the right answer")
	}
	if c.Check(ch, a) {
		t.Error("the same challenge twice")
	}
	other := &Checker{Key: []byte("other")}
	ch2 := other.Challenge()
	if c.Check(ch2, solve(ch2)) {
		t.Error("a challenge signed elsewhere")
	}
}
