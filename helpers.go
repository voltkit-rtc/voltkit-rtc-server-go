package voltkit

import (
	"fmt"
	"math/big"
	"strings"
)

// Ptr returns a pointer to v: for the optional fields that distinguish "left
// out" from zero, Config.Retries (voltkit.Ptr(0) turns retries off) and
// VerifyOptions.Tolerance.
func Ptr[T any](v T) *T { return &v }

// CuPerCredit is how many credit units (cu) one credit is. Every cu amount on
// the wire is a decimal string; see ParseCu.
const CuPerCredit = 6_000_000

// ParseCu reads a cu amount as the wire sends it ("186000000", a decimal
// string, never a number: a float would round a large balance) into a
// *big.Int for exact arithmetic. It refuses anything but an optional sign and
// decimal digits.
func ParseCu(amount string) (*big.Int, error) {
	s := strings.TrimSpace(amount)
	digits := strings.TrimPrefix(strings.TrimPrefix(s, "-"), "+")
	if digits == "" || strings.Trim(digits, "0123456789") != "" {
		return nil, fmt.Errorf("voltkit: %q is not a cu amount (a decimal integer as a string)", amount)
	}
	n, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return nil, fmt.Errorf("voltkit: %q is not a cu amount (a decimal integer as a string)", amount)
	}
	return n, nil
}
