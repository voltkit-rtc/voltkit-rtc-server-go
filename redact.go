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
// second-layer escapers key on.
type piece struct {
	char byte
	a, b byte
}

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
	if at+len(alt) > len(text) {
		return 0, false
	}
	for i, p := range alt {
		c := text[at+i]
		if c != p.a && c != p.b {
			return 0, false
		}
	}
	return at + len(alt), true
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

// hexReferenceOf is the HTML reference &#xH;, x and hex digits in either case.
func hexReferenceOf(code int) alternative {
	return concat(exact("&#"), alternative{{char: 'x', a: 'x', b: 'X'}}, hexPieces(code, 1), exact(";"))
}

// decimalReferenceOf is &#D;, zero-padded to width (PHP writes &#039;).
func decimalReferenceOf(code int, width int) alternative {
	d := itoa(code)
	for len(d) < width {
		d = "0" + d
	}
	return exact("&#" + d + ";")
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
	// json.Marshal writes & as & too; PHP's json_encode writes / as \/.
	{'\\': exact(`\\`), '"': exact(`\"`), '&': exact(`&`)},
	{'\\': exact(`\\`), '"': exact(`\"`), '/': exact(`\/`)},
	// HTML: html.escape, htmlspecialchars and lodash write &quot;, Go &#34;.
	{'&': exact("&amp;"), '"': exact("&quot;")},
	{'&': exact("&amp;"), '"': exact("&#34;")},
	escaping(escapeCharacters, func(code int) alternative { return exact("&" + htmlNames[byte(code)][0] + ";") }),
	escaping(escapeCharacters, hexReferenceOf),
	escaping(escapeCharacters, func(code int) alternative { return decimalReferenceOf(code, 1) }),
}

// oneLayerForms is every form one escaper gives ch: itself; its byte as %XX
// in either hex case; "+" for a space (form encoding); its JSON escapes
// (JSON.stringify's \" and \t, PHP's \/, \uXXXX); and its HTML references:
// each name, &#D; (and PHP's &#039;), &#xH; in either case.
func oneLayerForms(ch byte) []alternative {
	code := int(ch)
	forms := []alternative{
		exact(string(ch)),
		percentOf(code),
		concat(exact(`\u`), hexPieces(code, 4)),
		decimalReferenceOf(code, 1),
		decimalReferenceOf(code, 3),
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
		b = append(b, p.a, p.b)
	}
	return string(b)
}

// --- URL credentials ---------------------------------------------------------

// redactURLCredentials replaces scheme://user:pass@ with scheme://[redacted]@
// in text (Node's URL_CREDENTIALS). A scheme is a letter followed by letters,
// digits, "+", "." or "-", starting at the first letter of such a run, then
// "://", then the user info up to the last "@" before whitespace, "/", "?" or
// "#", so a password holding an @ is taken out whole. mailto:me@host and
// https://host/a@b are untouched.
func redactURLCredentials(text string) string {
	var out strings.Builder
	start := 0
	i := 0
	for {
		j := strings.Index(text[i:], "://")
		if j < 0 {
			break
		}
		j += i
		// The run of scheme characters before "://", and its first letter.
		runStart := j
		for runStart > 0 && isSchemeByte(text[runStart-1]) {
			runStart--
		}
		schemeStart := -1
		for k := runStart; k < j; k++ {
			if isASCIILetter(text[k]) {
				schemeStart = k
				break
			}
		}
		afterScheme := j + 3
		if schemeStart < 0 {
			i = afterScheme
			continue
		}
		// The user info: up to the last @ before a stop character.
		end := afterScheme
		for end < len(text) {
			r, size := utf8.DecodeRuneInString(text[end:])
			if r == '/' || r == '?' || r == '#' || unicode.IsSpace(r) {
				break
			}
			end += size
		}
		at := strings.LastIndexByte(text[afterScheme:end], '@')
		if at < 0 {
			i = afterScheme
			continue
		}
		at += afterScheme
		out.WriteString(text[start:afterScheme])
		out.WriteString(redactedMarker)
		out.WriteByte('@')
		start = at + 1
		i = start
	}
	if start == 0 {
		return text
	}
	out.WriteString(text[start:])
	return out.String()
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
