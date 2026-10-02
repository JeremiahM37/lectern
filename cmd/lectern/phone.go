package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/term"
	"rsc.io/qr"

	"github.com/JeremiahM37/lectern/v2/cmd/lectern/localruntime"
	"github.com/JeremiahM37/lectern/v2/internal/config"
)

// phoneCommand is `lectern phone`: let a phone on this Wi-Fi reach your
// private Lectern, and print a QR code that pairs it (localruntime/phone.go).
func phoneCommand(cfg *config.Config, args []string) error {
	off, showQR := false, term.IsTerminal(int(os.Stdout.Fd()))
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
	if os.Getenv("LECTERN_API") != "" {
		return errors.New("lectern phone sets up your private Lectern on this computer, but LECTERN_API points at a server elsewhere; open that server's web page and use Settings → Connect your phone")
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	ep, err := localruntime.Ensure(ctx, binary, cfg)
	if err != nil {
		return fmt.Errorf("start Lectern: %w", err)
	}
	st, err := localruntime.Phone(ctx, ep, !off)
	if err != nil {
		return err
	}
	if off {
		fmt.Println("Phones on this Wi-Fi can no longer reach Lectern. Paired phones stay paired for when you turn it back on.")
		return nil
	}
	printPhone(os.Stdout, st, showQR)
	return nil
}

func printPhone(w io.Writer, st localruntime.PhoneState, showQR bool) {
	fmt.Fprintf(w, "Lectern is reachable on this Wi-Fi at %s\n\n", st.URL)
	if showQR {
		if code, err := qr.Encode(st.PairURL, qr.M); err == nil {
			writeQR(w, code)
			fmt.Fprintln(w)
		}
	}
	fmt.Fprintln(w, "Scan this with your phone's camera, or open this link on the phone (it works once, for 5 minutes):")
	fmt.Fprintf(w, "  %s\n\n", st.PairURL)
	fmt.Fprintln(w, "⚠ "+st.Warning)
	fmt.Fprintln(w, "\nAway from this Wi-Fi: install Tailscale on this computer and the phone (encrypted, works anywhere),")
	fmt.Fprintln(w, "or run `lectern relay` on a server both can reach (docs/relay.md). Settings → Connect your phone shows both.")
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
