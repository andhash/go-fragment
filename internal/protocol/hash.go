package protocol

import "regexp"

// hashRe is the only HTML parse in the module.
var hashRe = regexp.MustCompile(`/api\?hash=([a-fA-F0-9]+)`)

// HashFromHTML returns the api hash embedded in a Fragment product page.
func HashFromHTML(html []byte) (string, bool) {
	m := hashRe.FindSubmatch(html)
	if m == nil {
		return "", false
	}
	return string(m[1]), true
}
