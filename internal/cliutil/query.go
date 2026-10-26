package cliutil

import (
	"fmt"
	"strings"
)

// This file builds openFDA Lucene search expressions. Every function here
// encodes a bug we already paid for once:
//
//   - AND / TO are emitted as LITERAL SPACES, never "+AND+" / "+TO+". The HTTP
//     layer builds the query with net/url (url.Values.Encode), which turns a
//     space into "+" on the wire — which openFDA reads as a term separator.
//     If we pre-baked a literal "+" it would be percent-encoded to %2B and
//     openFDA would treat it as a Lucene "+" (required-term) operator, breaking
//     the query. So: spaces in, let url.Values do the encoding. See TestLucene*.
//   - Date ranges use Lucene bracket syntax field:[YYYYMMDD TO YYYYMMDD].
//   - Class filters are a QUOTED STRING classification:"Class I", never an
//     array (an array returns zero hits).

// Phrase builds field:"term". Embedded double quotes and backslashes in the
// term are stripped: a quote could break out of the phrase, and a trailing
// backslash would escape the closing quote and leave the phrase unterminated.
func Phrase(field, term string) string {
	term = strings.NewReplacer(`"`, "", `\`, "").Replace(strings.TrimSpace(term))
	return fmt.Sprintf(`%s:"%s"`, field, term)
}

// And joins clauses with the Lucene AND operator using literal spaces. Empty
// clauses are dropped. With one clause it returns that clause unchanged.
func And(clauses ...string) string {
	kept := make([]string, 0, len(clauses))
	for _, c := range clauses {
		if strings.TrimSpace(c) != "" {
			kept = append(kept, c)
		}
	}
	return strings.Join(kept, " AND ")
}

// Or joins clauses with the Lucene OR operator using literal spaces.
func Or(clauses ...string) string {
	kept := make([]string, 0, len(clauses))
	for _, c := range clauses {
		if strings.TrimSpace(c) != "" {
			kept = append(kept, c)
		}
	}
	return strings.Join(kept, " OR ")
}

// DateRange builds field:[from TO to] with Lucene brackets and literal spaces.
// Dates are expected in openFDA's compact YYYYMMDD form.
func DateRange(field, from, to string) string {
	return fmt.Sprintf("%s:[%s TO %s]", field, strings.TrimSpace(from), strings.TrimSpace(to))
}

// classLabels maps the user-facing 1/2/3 to the FDA Roman-numeral label used
// server-side. Mapping happens here (server-side) so a page limit can never
// skew a client-side class filter.
var classLabels = map[int]string{1: "Class I", 2: "Class II", 3: "Class III"}

// ClassFilter builds classification:"Class N" for n in {1,2,3}. It returns an
// error for any other value; callers must validate --class explicitly.
func ClassFilter(class int) (string, error) {
	label, ok := classLabels[class]
	if !ok {
		return "", fmt.Errorf("class must be 1, 2, or 3 (got %d)", class)
	}
	return Phrase("classification", label), nil
}
