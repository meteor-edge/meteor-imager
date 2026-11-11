// Package shacrypt implements SHA-512 crypt ("$6$"), the password hash format of
// /etc/shadow, cloud-init's passwd and Raspberry Pi OS userconf.txt, so a login
// password reaches the card only as a hash.
//
// Algorithm: Ulrich Drepper, "Unix crypt using SHA-256 and SHA-512" (2008).
package shacrypt

import (
	"crypto/rand"
	"crypto/sha512"
	"crypto/subtle"
	"errors"
	"strconv"
	"strings"
)

const (
	alphabet      = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	defaultRounds = 5000
	maxSaltLen    = 16
)

// Hash returns the SHA-512 crypt hash of password with a random 16-character salt.
func Hash(password string) (string, error) {
	raw := make([]byte, maxSaltLen)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	salt := make([]byte, maxSaltLen)
	for i, b := range raw {
		salt[i] = alphabet[int(b)%len(alphabet)]
	}
	return Crypt(password, "$6$"+string(salt)), nil
}

// Crypt computes SHA-512 crypt for a setting "$6$salt" or "$6$rounds=N$salt"
// (anything after the salt is ignored, so a full hash works as the setting).
func Crypt(password, setting string) string {
	rounds, salt, explicit := parseSetting(setting)
	sum := compute([]byte(password), []byte(salt), rounds)
	var b strings.Builder
	b.WriteString("$6$")
	if explicit {
		b.WriteString("rounds=" + strconv.Itoa(rounds) + "$")
	}
	b.WriteString(salt + "$")
	b.WriteString(encode(sum))
	return b.String()
}

// Verify reports whether password matches a "$6$" hash.
func Verify(password, hash string) (bool, error) {
	if !strings.HasPrefix(hash, "$6$") {
		return false, errors.New("not a SHA-512 crypt hash")
	}
	got := Crypt(password, hash)
	return subtle.ConstantTimeCompare([]byte(got), []byte(hash)) == 1, nil
}

func parseSetting(setting string) (rounds int, salt string, explicit bool) {
	rest := strings.TrimPrefix(setting, "$6$")
	rounds = defaultRounds
	if strings.HasPrefix(rest, "rounds=") {
		if i := strings.IndexByte(rest, '$'); i > 0 {
			if n, err := strconv.Atoi(rest[len("rounds="):i]); err == nil {
				rounds = min(max(n, 1000), 999999999)
				explicit = true
				rest = rest[i+1:]
			}
		}
	}
	if i := strings.IndexByte(rest, '$'); i >= 0 {
		rest = rest[:i]
	}
	if len(rest) > maxSaltLen {
		rest = rest[:maxSaltLen]
	}
	return rounds, rest, explicit
}

func compute(key, salt []byte, rounds int) []byte {
	h := sha512.New()
	h.Write(key)
	h.Write(salt)
	h.Write(key)
	alt := h.Sum(nil)

	h.Reset()
	h.Write(key)
	h.Write(salt)
	h.Write(repeat(alt, len(key)))
	for n := len(key); n > 0; n >>= 1 {
		if n&1 != 0 {
			h.Write(alt)
		} else {
			h.Write(key)
		}
	}
	a := h.Sum(nil)

	h.Reset()
	for range key {
		h.Write(key)
	}
	p := repeat(h.Sum(nil), len(key))

	h.Reset()
	for i := 0; i < 16+int(a[0]); i++ {
		h.Write(salt)
	}
	s := repeat(h.Sum(nil), len(salt))

	for i := 0; i < rounds; i++ {
		h.Reset()
		if i&1 != 0 {
			h.Write(p)
		} else {
			h.Write(a)
		}
		if i%3 != 0 {
			h.Write(s)
		}
		if i%7 != 0 {
			h.Write(p)
		}
		if i&1 != 0 {
			h.Write(a)
		} else {
			h.Write(p)
		}
		a = h.Sum(a[:0])
	}
	return a
}

// repeat returns n bytes of b repeated.
func repeat(b []byte, n int) []byte {
	out := make([]byte, 0, n)
	for len(out) < n {
		out = append(out, b[:min(len(b), n-len(out))]...)
	}
	return out
}

// order is the byte permutation SHA-512 crypt uses before base64 encoding.
var order = [21][3]int{
	{0, 21, 42}, {22, 43, 1}, {44, 2, 23}, {3, 24, 45}, {25, 46, 4}, {47, 5, 26}, {6, 27, 48},
	{28, 49, 7}, {50, 8, 29}, {9, 30, 51}, {31, 52, 10}, {53, 11, 32}, {12, 33, 54}, {34, 55, 13},
	{56, 14, 35}, {15, 36, 57}, {37, 58, 16}, {59, 17, 38}, {18, 39, 60}, {40, 61, 19}, {62, 20, 41},
}

func encode(sum []byte) string {
	var b strings.Builder
	put := func(w uint32, n int) {
		for ; n > 0; n-- {
			b.WriteByte(alphabet[w&0x3f])
			w >>= 6
		}
	}
	for _, o := range order {
		put(uint32(sum[o[0]])<<16|uint32(sum[o[1]])<<8|uint32(sum[o[2]]), 4)
	}
	put(uint32(sum[63]), 2)
	return b.String()
}
