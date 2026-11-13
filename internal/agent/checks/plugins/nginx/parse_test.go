package nginx

import "testing"

func TestParseStatus(t *testing.T) {
	got, err := parseStatus([]byte("Active connections: 291 \nserver accepts handled requests\n 16630948 16630947 31070465 \nReading: 6 Writing: 179 Waiting: 106 \n"))
	if err != nil {
		t.Fatal(err)
	}
	want := status{active: 291, accepts: 16630948, handled: 16630947, requests: 31070465, reading: 6, writing: 179, waiting: 106}
	if got != want {
		t.Fatalf("parseStatus = %+v, want %+v", got, want)
	}
}
