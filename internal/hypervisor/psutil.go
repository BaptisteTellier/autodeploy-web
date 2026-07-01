package hypervisor

import "strings"

// psLit escapes a string for safe embedding inside a PowerShell SINGLE-QUOTED
// literal — i.e. between the quotes of '...'. In a single-quoted PS string the
// only metacharacter is the single quote itself, which is escaped by doubling
// it (”). This both prevents a value with an apostrophe from breaking the
// script (a correctness bug) and closes off PowerShell injection via values
// interpolated into '%s' slots (defence in depth — most such values are
// operator-supplied names/paths that are not otherwise quote-validated).
//
// Do NOT use this for values placed inside a double-quoted PS string ("...",
// where $ and ` are also special) or inside an @'...'@ here-string (where a
// single quote is literal and must NOT be doubled).
func psLit(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}
