package fragment

import (
	"regexp"
	"strings"
	"unicode"
)

// Product identifies a Fragment purchase. Later products can use new values
// without changing Order.
type Product string

const (
	ProductStars   Product = "stars"
	ProductPremium Product = "premium"
)

// State is the durable payment state. nil from Pay is not delivery unless
// the state is confirmed or settled.
type State string

const (
	StatePreparing    State = "preparing"
	StatePrepared     State = "prepared"
	StateSigned       State = "signed"
	StateUnknown      State = "unknown"
	StateBroadcast    State = "broadcast"
	StateConfirmed    State = "confirmed"
	StateSettled      State = "settled"
	StateNotBroadcast State = "not_broadcast"
)

// IdempotencyKey is an opaque caller string. The library does not interpret
// a storefront prefix. The journal file name is the SHA-256 of the key.
type IdempotencyKey string

// Validate checks length and visible ASCII. An empty key is ErrIdempotencyKeyRequired.
func (k IdempotencyKey) Validate() error {
	if k == "" {
		return ErrIdempotencyKeyRequired
	}
	if len(k) > 128 {
		return ErrIdempotencyKey
	}
	for _, r := range string(k) {
		if r > unicode.MaxASCII || r < 0x21 || r == ' ' || unicode.IsControl(r) {
			return ErrIdempotencyKey
		}
	}
	return nil
}

// Order is one purchase. ShowSender on a direct Prepare or Pay is taken from
// this struct. PrepareStars and PayStars copy it from WithShowSender.
type Order struct {
	Product        Product
	Username       string
	Stars          int
	Months         int
	ShowSender     bool
	IdempotencyKey IdempotencyKey
}

const (
	minStars = 50
	maxStars = 10_000_000
)

var usernameRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{4,31}$`)

var usernamePrefixes = []string{
	"https://t.me/",
	"http://t.me/",
	"https://telegram.me/",
	"http://telegram.me/",
	"t.me/",
	"telegram.me/",
}

// NormalizeUsername strips a t.me or telegram.me prefix and one leading @.
// A phone-like string is rejected before any network call.
func NormalizeUsername(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", ErrInvalidUsername
	}
	if looksLikePhone(s) {
		return "", ErrPhoneNotAccepted
	}
	for _, p := range usernamePrefixes {
		if strings.HasPrefix(s, p) {
			s = strings.TrimPrefix(s, p)
			break
		}
	}
	s = strings.TrimPrefix(s, "@")
	if looksLikePhone(s) {
		return "", ErrPhoneNotAccepted
	}
	if strings.ContainsAny(s, "/?#&") {
		return "", ErrInvalidUsername
	}
	if !usernameRE.MatchString(s) {
		return "", ErrInvalidUsername
	}
	return s, nil
}

func looksLikePhone(s string) bool {
	if s == "" {
		return false
	}
	if strings.HasPrefix(s, "+") {
		rest := s[1:]
		return rest != "" && allDigits(rest)
	}
	return len(s) > 10 && allDigits(s)
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (o Order) quantity() int {
	if o.Product == ProductPremium {
		return o.Months
	}
	return o.Stars
}

func validateOrderShape(o Order) error {
	if err := o.IdempotencyKey.Validate(); err != nil {
		return err
	}
	switch o.Product {
	case ProductStars:
		if o.Stars < minStars || o.Stars > maxStars {
			return ErrInvalidStarsAmount
		}
	case ProductPremium:
		if o.Months != 3 && o.Months != 6 && o.Months != 12 {
			return ErrInvalidMonths
		}
	default:
		return coded("invalid_product", "product must be stars or premium")
	}
	if _, err := NormalizeUsername(o.Username); err != nil {
		return err
	}
	return nil
}
