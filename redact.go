package voltkit

import (
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// Redaction (spec section 5.5): everything an error message or code repeats
// from someone else's words (the server's message and code, a reply that is
// not JSON, a redirect's Location, the transport's own error text) goes
// through here before it is used and before any cut. Two things are taken
// out: the API key, in any encoding a reply can hold, and the credentials of
// any URL (scheme://user:pass@host). The SDK's own words never contain the key.

const (
	// redactedMarker replaces the key, and a URL's credentials.
	redactedMarker = "[redacted]"
	// longestKeyLookedFor is the longest key the matcher is built for. Past
	// it the word rule applies instead (Node: the pattern it compiles would be
	// too large; here it keeps the two SDKs' behaviour the same).
	longestKeyLookedFor = 256
	// keyNotChecked replaces a word, or a whole message, that could hold a
	// key too long to look for.
	keyNotChecked = "[not shown: it could not be checked for the API key]"
)

// A piece is one position of an encoded form of a key character: the byte
// it must be, or either of two bytes where hex digits and the x of "&#x" may
// be in either case. char is the character the piece stands for, which the
// second-layer escapers key on. A zeros piece is a run of any number of "0",
// none included: the zeros a numeric reference may start with (&#x005F;,
// &#0095;, N049-5). A digit other than 0 always follows one, so the run
// matches one way and the match stays linear.
type piece struct {
	char  byte
	a, b  byte
	zeros bool
}

// zeros is the run of "0" before a numeric reference's digits.
var zeros = piece{char: '0', a: '0', b: '0', zeros: true}

// An alternative is one spelling of one key character: a fixed sequence of
// pieces. No two alternatives of a character match the same text, so a match
// covers the key in order and unrelated text is untouched.
type alternative []piece

// keyRedactor finds the key in any of its spellings, in time linear in the
// text: at each position the key's characters are matched one after another,
// each through its short list of alternatives, and the scan moves on as soon
// as one fails. The redactor is built once per client and read concurrently.
type keyRedactor struct {
	key string
	// alts[i] are the spellings of key[i].
	alts []([]alternative)
	// firstByte[b] is whether some spelling of key[0] starts with b: the
	// cheap test that rejects almost every position of a long reply.
	firstByte [256]bool
	// wordRule is set for a key longer than longestKeyLookedFor: then words
	// at least as long as the key are withheld instead (failing closed).
	wordRule bool
	// keyHasSpace: a key holding whitespace (a tab) could be split across
	// words, so under the word rule the whole message goes.
	keyHasSpace bool
}

// newKeyRedactor builds the redactor for the net key (whitespace at both ends
// already removed: each form of the key as given holds the net key, with the
// whitespace around it in the same form, so this one alone is looked for).
func newKeyRedactor(key string) *keyRedactor {
	r := &keyRedactor{key: key}
	if len(key) > longestKeyLookedFor {
		r.wordRule = true
		r.keyHasSpace = strings.IndexFunc(key, unicode.IsSpace) >= 0
		return r
	}
	r.alts = make([]([]alternative), len(key))
	for i := 0; i < len(key); i++ {
		r.alts[i] = alternativesOf(key[i])
	}
	for _, alt := range r.alts[0] {
		r.firstByte[alt[0].a] = true
		r.firstByte[alt[0].b] = true
	}
	return r
}

// redact returns text with every spelling of the key replaced by
// redactedMarker, or, for a key too long to look for, with every word that
// could hold it withheld.
func (r *keyRedactor) redact(text string) string {
	if r.wordRule {
		return r.withholdWords(text)
	}
	var out strings.Builder
	start := 0
	i := 0
	for i < len(text) {
		if r.firstByte[text[i]] {
			if end := r.matchAt(text, i); end > i {
				out.WriteString(text[start:i])
				out.WriteString(redactedMarker)
				i = end
				start = i
				continue
			}
		}
		i++
	}
	if start == 0 {
		return text
	}
	out.WriteString(text[start:])
	return out.String()
}

// matchAt returns where a match of the whole key starting at `from` ends, or
// -1. Two spellings of one character may be prefixes of each other ("%" as
// itself and "%25" as its URL encoding), so each step keeps every position
// the character could have ended at; the longest overall match wins, which
// for redaction is the safe side.
func (r *keyRedactor) matchAt(text string, from int) int {
	positions := []int{from}
	var next []int
	for _, alts := range r.alts {
		next = next[:0]
		for _, p := range positions {
			for _, alt := range alts {
				if end, ok := matchAlternative(text, p, alt); ok && !containsInt(next, end) {
					next = append(next, end)
				}
			}
		}
		if len(next) == 0 {
			return -1
		}
		positions, next = next, positions
	}
	end := -1
	for _, p := range positions {
		if p > end {
			end = p
		}
	}
	return end
}

func matchAlternative(text string, at int, alt alternative) (int, bool) {
	i := at
	for _, p := range alt {
		if p.zeros {
			for i < len(text) && text[i] == '0' {
				i++
			}
			continue
		}
		if i >= len(text) || (text[i] != p.a && text[i] != p.b) {
			return 0, false
		}
		i++
	}
	return i, true
}

func containsInt(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// withholdWords is the fail-closed rule for a key that is not looked for:
// every form of the key is at least as long as the key and, when the key
// holds no whitespace, lies within one word (a run of non-whitespace), so
// words at least as long as the key are replaced by keyNotChecked and shorter
// ones, such as error codes, are kept (Node N046-1, N047-1). A key holding a
// tab could be split across words: then the whole text goes, unless it is
// shorter than the key.
func (r *keyRedactor) withholdWords(text string) string {
	keyLen := utf8.RuneCountInString(r.key)
	if r.keyHasSpace {
		if utf8.RuneCountInString(text) < keyLen {
			return text
		}
		return keyNotChecked
	}
	var out strings.Builder
	wordStart := -1
	wordRunes := 0
	flush := func(end int) {
		if wordStart < 0 {
			return
		}
		if wordRunes >= keyLen {
			out.WriteString(keyNotChecked)
		} else {
			out.WriteString(text[wordStart:end])
		}
		wordStart = -1
		wordRunes = 0
	}
	for i, c := range text {
		if unicode.IsSpace(c) {
			flush(i)
			out.WriteRune(c)
			continue
		}
		if wordStart < 0 {
			wordStart = i
		}
		wordRunes++
	}
	flush(len(text))
	return out.String()
}

// --- the spellings of one character ------------------------------------------

func exact(s string) alternative {
	alt := make(alternative, len(s))
	for i := 0; i < len(s); i++ {
		alt[i] = piece{char: s[i], a: s[i], b: s[i]}
	}
	return alt
}

// hexPieces is code in upper-case hex, zero-padded to width, each letter
// allowed in either case.
func hexPieces(code int, width int) alternative {
	digits := strings.ToUpper(itoaBase16(code))
	for len(digits) < width {
		digits = "0" + digits
	}
	alt := make(alternative, 0, len(digits))
	for i := 0; i < len(digits); i++ {
		d := digits[i]
		if 'A' <= d && d <= 'F' {
			alt = append(alt, piece{char: d, a: d, b: d + ('a' - 'A')})
		} else {
			alt = append(alt, piece{char: d, a: d, b: d})
		}
	}
	return alt
}

func itoaBase16(code int) string {
	const digits = "0123456789abcdef"
	if code == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for code > 0 {
		i--
		buf[i] = digits[code&15]
		code >>= 4
	}
	return string(buf[i:])
}

func itoa(code int) string {
	if code == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for code > 0 {
		i--
		buf[i] = byte('0' + code%10)
		code /= 10
	}
	return string(buf[i:])
}

func concat(parts ...alternative) alternative {
	var out alternative
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// percentOf is %XX, hex in either case: encodeURIComponent, URLSearchParams,
// Python's quote and hand-rolled encoders.
func percentOf(code int) alternative { return concat(exact("%"), hexPieces(code, 2)) }

// hexReferenceOf is the HTML reference &#xH;, x and hex digits in either case,
// with any number of zeros before the digits (&#x5F;, &#X005f;).
func hexReferenceOf(code int) alternative {
	return concat(exact("&#"), alternative{{char: 'x', a: 'x', b: 'X'}, zeros}, hexPieces(code, 1), exact(";"))
}

// decimalReferenceOf is &#D;, with any number of zeros before the digits
// (&#95;, PHP's &#039;, &#00000095;).
func decimalReferenceOf(code int) alternative {
	return concat(exact("&#"), alternative{zeros}, exact(itoa(code)+";"))
}

// htmlNames are the HTML5 named references of the characters a key may hold,
// and of the backslash, which escapes hold.
var htmlNames = map[byte][]string{
	'\t': {"Tab"}, '!': {"excl"}, '"': {"quot", "QUOT"}, '#': {"num"}, '$': {"dollar"}, '%': {"percnt"},
	'&': {"amp", "AMP"}, '\'': {"apos"}, '(': {"lpar"}, ')': {"rpar"}, '*': {"ast", "midast"}, '+': {"plus"},
	',': {"comma"}, '.': {"period"}, '/': {"sol"}, ':': {"colon"}, ';': {"semi"}, '<': {"lt", "LT"}, '=': {"equals"},
	'>': {"gt", "GT"}, '?': {"quest"}, '@': {"commat"}, '[': {"lsqb", "lbrack"}, '\\': {"bsol"}, ']': {"rsqb", "rbrack"},
	'^': {"Hat"}, '_': {"lowbar", "UnderBar"}, '`': {"grave", "DiacriticalGrave"}, '{': {"lcub", "lbrace"},
	'|': {"verbar", "vert", "VerticalLine"}, '}': {"rcub", "rbrace"},
}

// escapeCharacters are what one-layer escapes are made of, other than letters
// and digits: all a second escaper changes.
const escapeCharacters = "%\\\"&#;/+"

type escaper map[byte]alternative

func escaping(chars string, encode func(code int) alternative) escaper {
	e := escaper{}
	for i := 0; i < len(chars); i++ {
		e[chars[i]] = encode(int(chars[i]))
	}
	return e
}

// escapers are the standard escapers, each as what it makes of the escape
// characters: a second layer is one run over a first (spec 5.5 item 5).
var escapers = []escaper{
	// URL: encodeURIComponent, URLSearchParams and Python's quote(safe='')
	// encode each; Python's quote() and encodeURI keep /.
	escaping(escapeCharacters, percentOf),
	escaping("%\\\"&#;+", percentOf),
	// JSON: JSON.stringify and json.dumps write \ as \\ and " as \"; Go's
	// json.Marshal writes & as \u0026 too (written out here in Go's own
	// escape; until 0.1.3 this entry was "&", which escaped nothing, and the
	// key as sk\u0026lowbar;... went through whole, G012-2); PHP's
	// json_encode writes / as \/.
	{'\\': exact(`\\`), '"': exact(`\"`), '&': exact(`\u0026`)},
	{'\\': exact(`\\`), '"': exact(`\"`), '/': exact(`\/`)},
	// HTML: html.escape, htmlspecialchars and lodash write &quot;, Go &#34;.
	{'&': exact("&amp;"), '"': exact("&quot;")},
	{'&': exact("&amp;"), '"': exact("&#34;")},
	escaping(escapeCharacters, func(code int) alternative { return exact("&" + htmlNames[byte(code)][0] + ";") }),
	escaping(escapeCharacters, hexReferenceOf),
	escaping(escapeCharacters, decimalReferenceOf),
}

// oneLayerForms is every form one escaper gives ch: itself; its byte as %XX
// in either hex case; "+" for a space (form encoding); its JSON escapes
// (JSON.stringify's \" and \t, PHP's \/, \uXXXX); and its HTML references:
// each name, &#D; and &#xH; with any number of zeros before the digits (PHP's
// &#039;), x and hex digits in either case.
func oneLayerForms(ch byte) []alternative {
	code := int(ch)
	forms := []alternative{
		exact(string(ch)),
		percentOf(code),
		concat(exact(`\u`), hexPieces(code, 4)),
		decimalReferenceOf(code),
		hexReferenceOf(code),
	}
	for _, name := range htmlNames[ch] {
		forms = append(forms, exact("&"+name+";"))
	}
	if ch == ' ' {
		forms = append(forms, exact("+"))
	}
	switch ch {
	case '"':
		forms = append(forms, exact(`\"`))
	case '\\':
		forms = append(forms, exact(`\\`))
	case '\t':
		forms = append(forms, exact(`\t`))
	case '/':
		forms = append(forms, exact(`\/`))
	}
	return forms
}

var (
	alternativesMu    sync.Mutex
	alternativesCache = map[byte][]alternative{}
)

// alternativesOf is every spelling of ch: each one-layer form, and each of
// those run through every standard escaper once more (%252F, \\\/, \&quot;,
// &amp;quot;), without duplicates. Built on demand and cached: there are at
// most 96 characters, and a client is often made per request.
func alternativesOf(ch byte) []alternative {
	alternativesMu.Lock()
	defer alternativesMu.Unlock()
	if cached, ok := alternativesCache[ch]; ok {
		return cached
	}
	seen := map[string]bool{}
	var out []alternative
	for _, form := range oneLayerForms(ch) {
		for _, esc := range append([]escaper{nil}, escapers...) {
			var mapped alternative
			for _, p := range form {
				if esc != nil {
					if rep, ok := esc[p.char]; ok {
						mapped = append(mapped, rep...)
						continue
					}
				}
				mapped = append(mapped, p)
			}
			key := alternativeKey(mapped)
			if !seen[key] {
				seen[key] = true
				out = append(out, mapped)
			}
		}
	}
	alternativesCache[ch] = out
	return out
}

func alternativeKey(alt alternative) string {
	b := make([]byte, 0, len(alt)*2)
	for _, p := range alt {
		if p.zeros {
			b = append(b, 0, 0) // no key byte is NUL
			continue
		}
		b = append(b, p.a, p.b)
	}
	return string(b)
}

// --- URL credentials ---------------------------------------------------------

// redactURLCredentials replaces the user info of every URL in text with
// [redacted]: scheme://user:pass@ becomes scheme://[redacted]@ (spec section
// 5.5, D7). A scheme is a letter followed by letters, digits, "+", "." or "-",
// then "://". A password may hold "@", "/", "?" or "#" (N049-3), so the user
// info is either
//
//   - anything without whitespace, "/", "?" or "#", up to the last "@" before
//     them; or, when there is none,
//   - "name:", then something that is not a port (not digits followed by "/",
//     "?", "#", whitespace or the end), then anything without whitespace, up
//     to the last "@".
//
// That is the Node SDK's pattern
// (?i)([a-z][a-z0-9+.-]*://)(?:[^\s/?#]*@|[^\s/?#:]*:(?!\d*(?:[/?#\s]|$))\S*@)
// replaced by $1[redacted]@, which RE2 cannot write (it has no lookahead): this
// is that pattern as code, with the same results (TestD7_URLCredentialsAgree
// WithThePattern, FuzzURLCredentials), whitespace being JavaScript's \s. So
// https://host/a@b, https://host:8080/a@b, https://host?e=a@b and
// mailto:me@host are left alone, and https://user:123/x@host, which reads as
// a port and a path, is shown.
//
// Linear in the text, as Node's per-word run is: a match lies within one word
// (a run of non-whitespace) and ends at an "@", so each word is read up to its
// last "@" and a word with none is passed over. The second form always ends
// at that last "@"; the first is found by one scan to the first "/", "?" or
// "#", and the next "://" lies past that scan's end, so no text is read twice
// for long. Run on a whole page from every "name:", the pattern took minutes.
func redactURLCredentials(text string) string {
	if !strings.Contains(text, "://") || strings.IndexByte(text, '@') < 0 {
		return text
	}
	var out strings.Builder
	written := 0 // text[:written] is in out
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		if isPatternSpace(r) {
			i += size
			continue
		}
		end := i
		for end < len(text) {
			r, size := utf8.DecodeRuneInString(text[end:])
			if isPatternSpace(r) {
				break
			}
			end += size
		}
		if last := strings.LastIndexByte(text[i:end], '@'); last >= 0 {
			for _, m := range credentialsIn(text[i : i+last+1]) {
				out.WriteString(text[written : i+m.userinfo])
				out.WriteString(redactedMarker)
				out.WriteByte('@')
				written = i + m.end
			}
		}
		i = end
	}
	if written == 0 {
		return text
	}
	out.WriteString(text[written:])
	return out.String()
}

// credential is one match in a word: the user info starts at userinfo and the
// match ends at end, just after its "@".
type credential struct{ userinfo, end int }

// credentialsIn finds the matches of the pattern in word, which holds no
// whitespace and ends at its last "@".
func credentialsIn(word string) []credential {
	var found []credential
	for from := 0; ; {
		j := strings.Index(word[from:], "://")
		if j < 0 {
			return found
		}
		j += from
		userinfo := j + 3
		// A scheme: the run of scheme characters before "://" holds a letter.
		// (The pattern starts at the run's first letter; where it starts
		// changes nothing, as the scheme is kept as it is.)
		hasLetter := false
		for k := j; k > from && isSchemeByte(word[k-1]); k-- {
			if isASCIILetter(word[k-1]) {
				hasLetter = true
			}
		}
		if !hasLetter {
			from = userinfo
			continue
		}
		// The first form: up to the last "@" before "/", "?" or "#".
		stop := strings.IndexAny(word[userinfo:], "/?#")
		if stop < 0 {
			stop = len(word)
		} else {
			stop += userinfo
		}
		if at := strings.LastIndexByte(word[userinfo:stop], '@'); at >= 0 {
			found = append(found, credential{userinfo, userinfo + at + 1})
			from = userinfo + at + 1
			continue
		}
		// The second form: a name without ":", then ":" and no port, then up
		// to the word's last "@", which is its end.
		colon := strings.IndexByte(word[userinfo:stop], ':')
		if colon >= 0 {
			after := userinfo + colon + 1
			d := after
			for d < len(word) && '0' <= word[d] && word[d] <= '9' {
				d++
			}
			if d < len(word) && word[d] != '/' && word[d] != '?' && word[d] != '#' {
				return append(found, credential{userinfo, len(word)})
			}
		}
		from = userinfo
	}
}

// isPatternSpace is JavaScript's \s, the whitespace of the Node SDK's pattern:
// Unicode's space separators, tab, line feed, vertical tab, form feed,
// carriage return, U+2028, U+2029 and U+FEFF. Go's unicode.IsSpace differs on
// two: it counts U+0085 and not U+FEFF. Either way a URL is cut at a space,
// and the results here must be Node's.
func isPatternSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return 0x2000 <= r && r <= 0x200A
}

func isASCIILetter(c byte) bool { return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') }

func isSchemeByte(c byte) bool {
	return isASCIILetter(c) || ('0' <= c && c <= '9') || c == '+' || c == '.' || c == '-'
}

// cutRunes returns the first n characters of s: the cuts of section 3.6 and
// 3.5 (300, 100, 200), applied after redaction so a cut cannot leave part of
// the key behind.
func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i := 0
	for k := 0; k < n; k++ {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return s[:i]
}
