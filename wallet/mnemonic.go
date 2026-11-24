package wallet

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha512"
	"errors"
	"strings"

	"golang.org/x/crypto/pbkdf2"
)

const (
	iterations = 100000
	salt       = "TON default seed"
	basicSalt  = "TON seed version"
)

// parseMnemonic accepts a TON 24-word mnemonic and returns the private key.
// 12, 15, 18, and 21 words are rejected. The key does not leave this process.
func parseMnemonic(mnemonic string) (ed25519.PrivateKey, error) {
	parts := strings.Fields(mnemonic)
	if len(parts) != 24 {
		return nil, errors.New("mnemonic must be 24 words")
	}
	for _, w := range parts {
		if !words[w] {
			return nil, errors.New("unknown mnemonic word")
		}
	}
	seedBytes := []byte(strings.Join(parts, " "))
	mac := hmac.New(sha512.New, seedBytes)
	mac.Write([]byte(""))
	hash := mac.Sum(nil)
	check := pbkdf2.Key(hash, []byte(basicSalt), iterations/256, 1, sha512.New)
	if check[0] != 0 {
		return nil, errors.New("invalid mnemonic checksum")
	}
	key := pbkdf2.Key(hash, []byte(salt), iterations, 32, sha512.New)
	return ed25519.NewKeyFromSeed(key), nil
}
