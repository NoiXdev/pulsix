package redis

import (
	"bufio"
	"errors"
	"strings"
	"testing"
)

func readOne(in string) (reply, error) {
	return readReply(bufio.NewReaderSize(strings.NewReader(in), maxLine))
}

func TestReadReply(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want reply
	}{
		{"simple string", "+OK\r\n", reply{kind: '+', text: "OK"}},
		{"integer", ":42\r\n", reply{kind: ':', n: 42}},
		{"bulk", "$5\r\nhello\r\n", reply{kind: '$', bulk: "hello"}},
		{"empty bulk", "$0\r\n\r\n", reply{kind: '$'}},
		{"bulk with CRLF inside", "$4\r\na\r\nb\r\n", reply{kind: '$', bulk: "a\r\nb"}},
		{"null bulk", "$-1\r\n", reply{kind: '$', null: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readOne(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestReadReplyErrorsAreClassifiedByPrefixOnly(t *testing.T) {
	tests := map[string]string{
		"-NOAUTH Authentication required.\r\n":                            "authentication failed",
		"-WRONGPASS invalid username-password pair\r\n":                   "authentication failed",
		"-ERR invalid password\r\n":                                       "authentication failed",
		"-ERR Client sent AUTH, but no password is set\r\n":               "authentication failed",
		"-ERR AUTH <password> called without any password configured\r\n": "authentication failed",
		"-NOPERM this user has no permissions to run 'info'\r\n":          "permission denied",
		"-ERR unknown command\r\n":                                        "server error",
		"-LOADING Redis is loading the dataset in memory\r\n":             "server error",
		"-\r\n": "server error",
	}
	for in, want := range tests {
		_, err := readOne(in)
		var se *serverError
		if !errors.As(err, &se) || se.class != want {
			t.Errorf("%q: err = %v, want class %q", in, err, want)
		}
		if err != nil && strings.Contains(err.Error(), "required") {
			t.Errorf("%q: reply text in %q", in, err)
		}
	}
}

func TestReadReplyRefusesWhatItCannotBound(t *testing.T) {
	tests := map[string]string{
		"oversized bulk":      "$999999999999\r\n",
		"just over the limit": "$1048577\r\n",
		"overflowing length":  "$99999999999999999999999999\r\n",
		"negative length":     "$-2\r\n",
		"not a number":        "$abc\r\n",
		"empty length":        "$\r\n",
		"array":               "*2\r\n$1\r\na\r\n$1\r\nb\r\n",
		"unknown type":        "HTTP/1.1 400 Bad Request\r\n",
		"integer garbage":     ":x\r\n",
		"no CRLF after bulk":  "$3\r\nabcXY",
		"truncated bulk":      "$10\r\nabc",
		"bare LF line":        "+OK\n",
		"empty":               "",
		"line too long":       "+" + strings.Repeat("a", maxLine) + "\r\n",
	}
	for name, in := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := readOne(in)
			if err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestReadReplyTakesABulkAtTheLimit(t *testing.T) {
	body := strings.Repeat("x", maxBulk)
	got, err := readOne("$1048576\r\n" + body + "\r\n")
	if err != nil || len(got.bulk) != maxBulk {
		t.Fatalf("len = %d, err = %v", len(got.bulk), err)
	}
}
