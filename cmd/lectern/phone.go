package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"golang.org/x/term"
	"rsc.io/qr"

	"github.com/JeremiahM37/lectern/v2/internal/console"
)

// phoneWarning is said wherever a phone is paired over plain HTTP.
const phoneWarning = "This address is not encrypted (plain HTTP on your network). Only paired devices can use it, " +
	"but use it on a network you trust."

// phoneCommand is `lectern phone`: pair a phone with this Lectern. It uses an
// address a phone can already reach (the tailnet, or a server's own network
// address); the private local runtime, which only answers this computer, is
// first made reachable on this computer's Wi-Fi address (POST
// /api/phone/wifi, cmd/lectern/localruntime/phone.go). --off stops that.
func phoneCommand(c *console.Client, args []string, out io.Writer) error {
	off, showQR := false, false
	if f, ok := out.(*os.File); ok {
		showQR = term.IsTerminal(int(f.Fd()))
	}
	for _, a := range args {
		switch a {
		case "--off":
			off = true
		case "--no-qr":
			showQR = false
		default:
			return errors.New("usage: lectern phone [--off] [--no-qr]")
		}
	}
	if off {
		if _, err := c.JSON("DELETE", "/phone/wifi", nil); err != nil {
			return err
		}
		fmt.Fprintln(out, "Phones on this Wi-Fi can no longer reach Lectern. Paired phones stay paired for when you turn it back on.")
		return nil
	}
	data, err := c.JSON("GET", "/phone/addresses", nil)
	if err != nil {
		return err
	}
	var state struct {
		CanEnable bool `json:"can_enable_wifi"`
		Options   []struct {
			Kind, URL string
			Available bool
		}
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}
	address := ""
	for _, o := range state.Options {
		if o.Available && (o.Kind == "tailnet" || o.Kind == "lan") {
			address = o.URL
			break
		}
	}
	if address == "" && state.CanEnable {
		data, err = c.JSON("POST", "/phone/wifi", nil)
		if err != nil {
			return err
		}
		var result struct{ URL string }
		if err := json.Unmarshal(data, &result); err != nil {
			return err
		}
		address = result.URL
	}
	if address == "" {
		return fmt.Errorf("no phone-accessible address is available; open Settings → Connect your phone for this server's network options")
	}
	if _, err := c.JSON("PUT", "/pair/settings", map[string]any{"enabled": true}); err != nil {
		return err
	}
	data, err = c.JSON("POST", "/pair/mint", nil)
	if err != nil {
		return err
	}
	var minted struct {
		Code string `json:"code"`
		TTL  int    `json:"ttl_s"`
	}
	if err := json.Unmarshal(data, &minted); err != nil {
		return err
	}
	link := address + "/pair#code=" + url.QueryEscape(minted.Code)
	fmt.Fprintf(out, "Lectern is reachable from your phone at %s\n\n", address)
	if showQR {
		if code, err := qr.Encode(link, qr.M); err == nil {
			writeQR(out, code)
			fmt.Fprintln(out)
		}
	}
	fmt.Fprintf(out, "Scan this with your phone's camera, or open this link on the phone (it works once, for %d seconds):\n", minted.TTL)
	fmt.Fprintf(out, "  %s\n\n", link)
	if u, _ := url.Parse(address); u != nil && u.Scheme == "http" {
		fmt.Fprintln(out, "⚠ "+phoneWarning)
		if state.CanEnable {
			fmt.Fprintln(out, "Turn it off with: lectern phone --off. It also stops when this Lectern stops.")
		}
		fmt.Fprintln(out, "\nAway from this Wi-Fi: install Tailscale on this computer and the phone (encrypted, works anywhere),")
		fmt.Fprintln(out, "or run `lectern relay` on a server both can reach (docs/relay.md). Settings → Connect your phone shows both.")
	}
	return nil
}

// writeQR draws a QR code with half blocks, two modules per character cell,
// dark on light with a quiet zone, so it scans on dark and light terminals.
func writeQR(w io.Writer, code *qr.Code) {
	const quiet = 2
	size := code.Size
	dark := func(x, y int) bool {
		return x >= 0 && y >= 0 && x < size && y < size && code.Black(x, y)
	}
	var b strings.Builder
	for y := -quiet; y < size+quiet; y += 2 {
		b.WriteString("\x1b[30;47m")
		for x := -quiet; x < size+quiet; x++ {
			top, bottom := dark(x, y), dark(x, y+1)
			switch {
			case top && bottom:
				b.WriteString("█")
			case top:
				b.WriteString("▀")
			case bottom:
				b.WriteString("▄")
			default:
				b.WriteString(" ")
			}
		}
		b.WriteString("\x1b[0m\n")
	}
	_, _ = io.WriteString(w, b.String())
}
