package cliutil

import (
	"net/url"
	"strings"
	"testing"
)

func TestPhrase(t *testing.T) {
	cases := []struct{ field, term, want string }{
		{"product_description", "pacemaker", `product_description:"pacemaker"`},
		{"recalling_firm", "Acme Medical", `recalling_firm:"Acme Medical"`},
		{"product_description", `bad"quote`, `product_description:"badquote"`},
		{"recalling_firm", "  trimmed  ", `recalling_firm:"trimmed"`},
		{"device.generic_name", `abc\`, `device.generic_name:"abc"`},
		{"device.generic_name", `a\"b`, `device.generic_name:"ab"`},
	}
	for _, c := range cases {
		if got := Phrase(c.field, c.term); got != c.want {
			t.Errorf("Phrase(%q,%q)=%q want %q", c.field, c.term, got, c.want)
		}
	}
}

func TestAndUsesLiteralSpaces(t *testing.T) {
	got := And("a:1", "b:2", "c:3")
	want := "a:1 AND b:2 AND c:3"
	if got != want {
		t.Fatalf("And=%q want %q", got, want)
	}
	if strings.Contains(got, "+AND+") {
		t.Fatalf("And must not emit literal +AND+: %q", got)
	}
	if And("only:1") != "only:1" {
		t.Errorf("single clause should pass through unchanged")
	}
	if And("a:1", "", "b:2") != "a:1 AND b:2" {
		t.Errorf("empty clauses must be dropped")
	}
}

func TestDateRangeBracketSyntax(t *testing.T) {
	got := DateRange("recall_initiation_date", "20240101", "20241231")
	want := "recall_initiation_date:[20240101 TO 20241231]"
	if got != want {
		t.Fatalf("DateRange=%q want %q", got, want)
	}
	if strings.Contains(got, "+TO+") {
		t.Fatalf("DateRange must not emit literal +TO+: %q", got)
	}
}

func TestClassFilterQuotedString(t *testing.T) {
	for n, want := range map[int]string{1: `classification:"Class I"`, 2: `classification:"Class II"`, 3: `classification:"Class III"`} {
		got, err := ClassFilter(n)
		if err != nil {
			t.Fatalf("ClassFilter(%d) unexpected err: %v", n, err)
		}
		if got != want {
			t.Errorf("ClassFilter(%d)=%q want %q", n, got, want)
		}
	}
	for _, bad := range []int{0, 4, -1} {
		if _, err := ClassFilter(bad); err == nil {
			t.Errorf("ClassFilter(%d) should error", bad)
		}
	}
}

// TestWireEncodingKeepsSpacesNotPlus is the load-bearing regression: once the
// query string goes through net/url, spaces must become "+" (a term separator
// openFDA understands) and there must be NO %2B — a %2B means we leaked a
// literal "+" and the query would break.
func TestWireEncodingKeepsSpacesNotPlus(t *testing.T) {
	class, _ := ClassFilter(1)
	search := And(Phrase("product_description", "pacemaker"), class,
		DateRange("recall_initiation_date", "20240101", "20241231"))

	v := url.Values{}
	v.Set("search", search)
	wire := v.Encode()

	if strings.Contains(wire, "%2B") {
		t.Fatalf("wire query leaked a literal '+' (%%2B): %s", wire)
	}
	// Spaces (AND / TO / the class label) must have become '+' on the wire.
	if !strings.Contains(wire, "Class+I") {
		t.Fatalf("expected space-encoded 'Class+I' on the wire, got: %s", wire)
	}
	if !strings.Contains(wire, "+AND+") {
		t.Fatalf("expected space-encoded '+AND+' separators on the wire, got: %s", wire)
	}
}
