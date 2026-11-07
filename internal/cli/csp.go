package cli

import "crypto/rand"

// Content-Security-Policy, phase 2: every <script> tag in web/index.html
// carries nonce="__CSP_NONCE__", and each index response replaces that marker
// with a fresh nonce named in the policy header. The policy is enforced: a
// script without the response's nonce is blocked.
//
// Style phase S1: the one <style> tag carries the same marker, and a separate
// Report-Only header reports (without blocking) styles that lack the nonce.
const (
	nonceMarker             = "__CSP_NONCE__"
	cspHeaderName           = "Content-Security-Policy"
	cspReportOnlyHeaderName = "Content-Security-Policy-Report-Only"
)

// nonceRandRead is the random source for the CSP nonce. It is a variable only
// so a test can make it fail.
var nonceRandRead = rand.Read

// contentSecurityPolicy is the policy for the index page. 'unsafe-inline' and
// https: are the backward-compatibility fallback for browsers that predate
// nonces and 'strict-dynamic'; browsers that understand nonce plus
// 'strict-dynamic' (Chrome 52+, Firefox 52+, Safari 15.4+) ignore both.
func contentSecurityPolicy(nonce string) string {
	return "script-src 'nonce-" + nonce + "' 'strict-dynamic' https: 'unsafe-inline'; script-src-attr 'none'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'"
}

// styleReportOnlyPolicy is the Report-Only style policy (style phase S1). It
// uses the same nonce as the enforced script policy.
func styleReportOnlyPolicy(nonce string) string {
	return "style-src 'nonce-" + nonce + "'; style-src-attr 'none'"
}
