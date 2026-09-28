package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/console"
)

func TestPhoneCommandEnablesWiFiAndPrintsExpiringPairingLink(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/phone/addresses":
			fmt.Fprint(w, `{"can_enable_wifi":true,"options":[]}`)
		case "/api/phone/wifi":
			fmt.Fprint(w, `{"url":"http://phone-test.invalid:1234"}`)
		case "/api/pair/settings":
			fmt.Fprint(w, `{"enabled":true}`)
		case "/api/pair/mint":
			fmt.Fprint(w, `{"code":"test-only-code","ttl_s":120}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	var out bytes.Buffer
	if err := phoneCommand(console.New(srv.URL, ""), nil, &out); err != nil {
		t.Fatal(err)
	}
	want := "GET /api/phone/addresses|POST /api/phone/wifi|PUT /api/pair/settings|POST /api/pair/mint"
	if strings.Join(calls, "|") != want {
		t.Fatalf("unexpected setup calls: %v", calls)
	}
	for _, word := range []string{"http://phone-test.invalid:1234/pair#code=test-only-code", "120 seconds", "unencrypted HTTP"} {
		if !strings.Contains(out.String(), word) {
			t.Fatalf("phone output omits %q", word)
		}
	}
}
