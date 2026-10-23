package voltkit

import (
	"fmt"
	"strings"
)

const upperHex = "0123456789ABCDEF"

// segment checks an id and returns it as one URL path segment (spec section
// 3.1). "", "." and ".." are refused before anything is sent: a URL reads
// them as steps through the path, spelt %2e or not, so no encoding keeps them
// in their segment, and DeleteRoom(ctx, "..") would otherwise send
// DELETE /v1/, with the key (Node test NODE-041-2). The refusal is an *Error
// of code bad-request and status 0, because it is about the call rather than
// the setup. Every other id is percent-encoded by encodeSegment.
func segment(name, id string) (string, error) {
	if id == "" || id == "." || id == ".." {
		return "", &Error{
			Code:      "bad-request",
			Status:    0,
			Message:   fmt.Sprintf("%s %q: an id may not be \"\", \".\" or \"..\", which a URL resolves to another endpoint; nothing was sent", name, id),
			Retryable: false,
		}
	}
	return encodeSegment(id), nil
}

// encodeSegment is JavaScript's encodeURIComponent: the UTF-8 bytes of the
// id, upper-case hex, every byte encoded except A-Z a-z 0-9 - _ . ~ and
// ! * ' ( ). The standard library's url.PathEscape is not it: that keeps
// $&+,;=:@, and a "+" or "@" in an identity would then reach the server in a
// different spelling than the other SDKs send (vectors ids.encoded).
func encodeSegment(id string) string {
	return percentEncode(id, "-_.~!*'()", false)
}

// encodeFormValue is what URLSearchParams does to a query value: the same
// rule with * - . _ kept and a space written as "+", for ListMessages's
// limit and before (spec section 3.1). url.QueryEscape differs on "*" and
// "~", which a message id never holds, but the vectors compare the whole URL.
func encodeFormValue(value string) string {
	return percentEncode(value, "*-._", true)
}

func percentEncode(s, safe string, spaceAsPlus bool) string {
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9', strings.IndexByte(safe, c) >= 0:
			b.WriteByte(c)
		case c == ' ' && spaceAsPlus:
			b.WriteByte('+')
		default:
			b.WriteByte('%')
			b.WriteByte(upperHex[c>>4])
			b.WriteByte(upperHex[c&15])
		}
	}
	return b.String()
}
