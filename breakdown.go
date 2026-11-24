package fragment

import (
	"math/big"
	"time"
)

// MessagePurpose says who added a wallet output. A donation row is added only
// by WithDonation. A donation inside the Fragment response is rejected.
type MessagePurpose string

const (
	PurposeFragment MessagePurpose = "fragment"
	PurposeDonation MessagePurpose = "donation"
)

// MessageLine is one output in the quote. Address is raw workchain:hex.
type MessageLine struct {
	Purpose    MessagePurpose `json:"purpose"`
	Address    string         `json:"address"`
	AmountNano int64          `json:"amount_nano"`
}

// AmountBreakdown is the quote shown before Sign. Amounts are integers.
// RequiredNanoTON counts each message once: sum(Messages)+GasReserveNanoTON.
// Gas is not its own message.
type AmountBreakdown struct {
	Product             Product
	Quantity            int
	PaymentNanoTON      int64
	GasReserveNanoTON   int64
	DonationNanoTON     int64
	DonationAddress     string
	DonationBasisPoints int
	Messages            []MessageLine
	RequiredNanoTON     int64
	ValidUntil          time.Time `json:"-"`
}

// PercentNano is (principal * basisPoints + 9999) / 10000.
// basisPoints must be in 0..1000. The function does not use floating point.
func PercentNano(principal int64, basisPoints int) (int64, error) {
	if principal < 0 || basisPoints < 0 || basisPoints > 1000 {
		return 0, ErrInvoiceMismatch
	}
	if basisPoints == 0 || principal == 0 {
		return 0, nil
	}
	p := new(big.Int).Mul(big.NewInt(principal), big.NewInt(int64(basisPoints)))
	p.Add(p, big.NewInt(9999))
	p.Quo(p, big.NewInt(10000))
	if !p.IsInt64() {
		return 0, ErrInvoiceMismatch
	}
	return p.Int64(), nil
}

func sumNano(values ...int64) (int64, error) {
	acc := new(big.Int)
	for _, v := range values {
		if v < 0 {
			return 0, ErrInvoiceMismatch
		}
		acc.Add(acc, big.NewInt(v))
	}
	if !acc.IsInt64() {
		return 0, ErrInvoiceMismatch
	}
	return acc.Int64(), nil
}

func quote(product Product, quantity int, principal, gas int64, lines []MessageLine, donationAddr string, basis int) (AmountBreakdown, error) {
	var msgSum int64
	var fragSum int64
	for _, ln := range lines {
		var err error
		msgSum, err = sumNano(msgSum, ln.AmountNano)
		if err != nil {
			return AmountBreakdown{}, err
		}
		if ln.Purpose == PurposeFragment {
			fragSum, err = sumNano(fragSum, ln.AmountNano)
			if err != nil {
				return AmountBreakdown{}, err
			}
		}
	}
	if principal > fragSum {
		return AmountBreakdown{}, ErrInvoiceMismatch
	}
	required, err := sumNano(msgSum, gas)
	if err != nil {
		return AmountBreakdown{}, err
	}
	var donation int64
	for _, ln := range lines {
		if ln.Purpose == PurposeDonation {
			donation = ln.AmountNano
		}
	}
	return AmountBreakdown{
		Product:             product,
		Quantity:            quantity,
		PaymentNanoTON:      principal,
		GasReserveNanoTON:   gas,
		DonationNanoTON:     donation,
		DonationAddress:     donationAddr,
		DonationBasisPoints: basis,
		Messages:            lines,
		RequiredNanoTON:     required,
	}, nil
}
