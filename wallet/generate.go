package wallet

import (
	"crypto/rand"
	"errors"
	"math/big"
	"sort"
	"strings"
)

var errMnemonic = errors.New("mnemonic")

// GenerateMnemonic returns a fresh 24-word TON mnemonic.
// It stays in this process. It is not a backup of a funded wallet.
func GenerateMnemonic() (string, error) {
	list := make([]string, 0, len(words))
	for w := range words {
		list = append(list, w)
	}
	sort.Strings(list)
	n := big.NewInt(int64(len(list)))
	for attempt := 0; attempt < 4096; attempt++ {
		picked := make([]string, 24)
		for i := range picked {
			x, err := rand.Int(rand.Reader, n)
			if err != nil {
				return "", err
			}
			picked[i] = list[x.Int64()]
		}
		phrase := strings.Join(picked, " ")
		if _, err := parseMnemonic(phrase); err == nil {
			return phrase, nil
		}
	}
	return "", errMnemonic
}
