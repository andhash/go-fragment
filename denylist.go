package fragment

import (
	"strings"

	"github.com/xssnick/tonutils-go/address"
)

// Denylist accounts. This file is the only place these addresses may appear.
// Comparison is raw workchain:hex, so UQ and EQ of the same account match.
const (
	// sweepFriendly is the 14.4.2 incident destination. It is never accepted.
	sweepFriendly = "UQAolfC7fyf__0H-1GzG9y5ZtV2CPC7yDno4W2cSVuYlJ6DU"
	// feeFriendly is the upstream FEE_ADDRESS. It is accepted only as an explicit WithDonation.
	feeFriendly = "UQAcsdD09x9dzj7Jc-MznN-SLUxPPMmwKQxsC2Ax_F03TBAH"
)

var (
	sweepRaw string
	feeRaw   string
)

func init() {
	var err error
	sweepRaw, err = NormalizeAddress(sweepFriendly)
	if err != nil {
		panic("fragment: sweep address: " + err.Error())
	}
	feeRaw, err = NormalizeAddress(feeFriendly)
	if err != nil {
		panic("fragment: fee address: " + err.Error())
	}
}

// NormalizeAddress parses a friendly or raw TON address into workchain:hex.
func NormalizeAddress(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ErrDestinationRejected
	}
	addr, err := address.ParseRawAddr(s)
	if err != nil {
		addr, err = address.ParseAddr(s)
	}
	if err != nil {
		return "", ErrDestinationRejected
	}
	raw := addr.StringRaw()
	if raw == "" || raw == "NONE" || raw == "NOT_SUPPORTED" {
		return "", ErrDestinationRejected
	}
	return raw, nil
}

func rejectFragment(raw string) error {
	if raw == sweepRaw || raw == feeRaw {
		return ErrDestinationRejected
	}
	return nil
}

func rejectDonation(raw string) error {
	if raw == sweepRaw {
		return ErrDestinationRejected
	}
	return nil
}
