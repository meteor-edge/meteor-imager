package shacrypt

import (
	"os/exec"
	"strings"
	"testing"
)

// Vectors from the specification, also produced by openssl passwd -6 and mkpasswd.
func TestCryptVectors(t *testing.T) {
	cases := []struct{ password, setting, want string }{
		{"Hello world!", "$6$saltstring",
			"$6$saltstring$svn8UoSVapNtMuq1ukKS4tPQd8iKwSMHWjl/O817G3uBnIFNjnQJuesI68u4OTLiBFdcbYEdFCoEOfaS35inz1"},
		{"Hello world!", "$6$rounds=10000$saltstringsaltstring",
			"$6$rounds=10000$saltstringsaltst$OW1/O6BYHV6BcXZu8QVeXbDWra3Oeqh0sbHbbMCVNSnCM/UrjmM0Dp8vOuZeHBy/YTBmSK6H9qs/y3RnOaw5v."},
		{"This is just a test", "$6$toolongsaltstring",
			"$6$toolongsaltstrin$lQ8jolhgVRVhY4b5pZKaysCLi0QBxGoNeKQzQ3glMhwllF7oGDZxUhx1yxdYcz/e1JSbq3y6JMxxl8audkUEm0"},
		{"p", "$6$abcdefgh",
			"$6$abcdefgh$cbHueVS/UT4Gg0.EnfnwJ27Lzzwf8rUdxRQSZm8iRDJv4TEZFHzD38nDTa.yVwJzbBrkaywt7RtORH2j7O12k1"},
	}
	for _, c := range cases {
		if got := Crypt(c.password, c.setting); got != c.want {
			t.Errorf("Crypt(%q, %q)\n got %s\nwant %s", c.password, c.setting, got, c.want)
		}
	}
}

func TestHashUsesARandomSaltAndVerifies(t *testing.T) {
	a, err := Hash("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Hash("correct horse")
	if a == b {
		t.Fatal("two hashes of the same password share a salt")
	}
	if !strings.HasPrefix(a, "$6$") || len(strings.Split(a, "$")[2]) != 16 {
		t.Fatalf("unexpected hash layout: %s", a)
	}
	if ok, _ := Verify("correct horse", a); !ok {
		t.Fatal("hash does not verify")
	}
	if ok, _ := Verify("wrong", a); ok {
		t.Fatal("wrong password verifies")
	}
}

// The system's own crypt (openssl) must accept what we produce.
func TestMatchesOpenSSL(t *testing.T) {
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl not installed")
	}
	for _, pw := range []string{"raspberry", "pässwörd with spaces", strings.Repeat("x", 100)} {
		h, err := Hash(pw)
		if err != nil {
			t.Fatal(err)
		}
		salt := strings.Split(h, "$")[2]
		// stdin, not an argument: Windows re-encodes non-ASCII command-line arguments.
		cmd := exec.Command("openssl", "passwd", "-6", "-salt", salt, "-stdin")
		cmd.Stdin = strings.NewReader(pw + "\n")
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(string(out)); got != h {
			t.Errorf("openssl: %s\n    ours: %s", got, h)
		}
	}
}
