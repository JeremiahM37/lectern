package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"

	"github.com/JeremiahM37/lectern/v2/internal/console"
	qrcode "github.com/skip2/go-qrcode"
)

func phoneCommand(c *console.Client, args []string, out io.Writer) error {
	if len(args) > 0 {
		return fmt.Errorf("usage: lectern phone")
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
	qr, err := qrcode.New(link, qrcode.Medium)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "Scan with your phone's camera. Keep both devices on the same network for a Wi-Fi address.")
	fmt.Fprintln(out, qr.ToSmallString(false))
	fmt.Fprintln(out, link)
	fmt.Fprintf(out, "This single-use pairing link expires in %d seconds.\n", minted.TTL)
	if u, _ := url.Parse(address); u != nil && u.Scheme == "http" {
		fmt.Fprintln(out, "Wi-Fi uses unencrypted HTTP; use this on a trusted network. Tailscale HTTPS is available in Settings.")
	}
	return nil
}
