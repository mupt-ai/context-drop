package mods

import "testing"

func TestCheckAllowsOnlyNamedCapabilities(t *testing.T) {
	for _, can := range [][]string{nil, {"list", "read", "send", "call", "kv"}} {
		if err := check(Meta{Name: "ok", Can: can}); err != nil {
			t.Errorf("can %v: %v", can, err)
		}
	}
	for _, bad := range []Meta{
		{Name: "Bad Name"},
		{Name: "m", Can: []string{"net:api.github.com"}},
		{Name: "m", Can: []string{"secret:token"}},
		{Name: "m", Can: []string{"ask"}},
	} {
		if err := check(bad); err == nil {
			t.Errorf("%+v was allowed", bad)
		}
	}
}
