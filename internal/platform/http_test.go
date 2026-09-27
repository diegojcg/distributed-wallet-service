package platform

import (
	"net/http/httptest"
	"testing"
)

func TestCanonicalRouteID(t *testing.T) {
	const canonical = "0192f291-27dd-7d3f-8071-5f8685deef37"
	for _, tc := range []struct {
		input string
		ok    bool
	}{
		{canonical, true}, {"0192F291-27DD-7D3F-8071-5F8685DEEF37", true},
		{"urn:uuid:" + canonical, true}, {"0192f29127dd7d3f80715f8685deef37", true},
		{"00000000-0000-0000-0000-000000000000", false}, {"invalid", false},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.SetPathValue("id", tc.input)
		w := httptest.NewRecorder()
		if got := validID(w, r); got != tc.ok {
			t.Fatalf("%s: accepted=%v", tc.input, got)
		}
		if tc.ok && r.PathValue("id") != canonical {
			t.Fatal(r.PathValue("id"))
		}
		if !tc.ok && w.Code != 400 {
			t.Fatal(w.Code)
		}
	}
}
