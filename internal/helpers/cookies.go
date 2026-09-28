package helpers

// browser-cookies is internal/browser's cookies.py for a cookies file: it
// reads (then deletes) a Netscape cookies.txt or JSON export on the
// browser's own machine and hands the cookies to the browser over its
// loopback DevTools websocket, printing only counts. Reading a Chrome
// profile needs its SQLite database, which the standard library cannot
// open, so that kind stays with the Python script.
//
//	lectern helper browser-cookies PORT WSPATH file PATH [DOMAINS]

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

func init() { Register("browser-cookies", browserCookies) }

// cookieExit is the script's `raise SystemExit(json.dumps({...}))`: the
// message goes to stderr and the exit status is 1.
type cookieExit struct{ msg string }

func (e *cookieExit) Error() string { return e.msg }

func cookieExitf(msg string) error { return &cookieExit{pyDumps(newObj("error", msg))} }

var sameSites = map[string]any{"strict": "Strict", "lax": "Lax", "none": "None", "no_restriction": "None", "unspecified": nil}

type cookieImport struct {
	domains []string
	skipped *pyObj
}

func (ci *cookieImport) skip(reason string) {
	n, _ := ci.skipped.Val(reason).(int)
	ci.skipped.Set(reason, n+1)
}

func (ci *cookieImport) wanted(domain string) bool {
	d := strings.ToLower(strings.TrimLeft(domain, "."))
	if len(ci.domains) == 0 {
		return true
	}
	for _, w := range ci.domains {
		if d == w || strings.HasSuffix(d, "."+w) {
			return true
		}
	}
	return false
}

func browserCookies(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) < 4 {
		return pyUncaught(stderr, "IndexError", errors.New("list index out of range"))
	}
	port, err := strconv.Atoi(wPyStrip(args[0]))
	if err != nil {
		return pyUncaught(stderr, "ValueError", errors.New("invalid literal for int() with base 10: "+pyReprString(args[0])))
	}
	wspath, kind, source := args[1], args[2], args[3]
	ci := &cookieImport{skipped: newObj()}
	if len(args) > 4 {
		for _, d := range strings.Split(args[4], ",") {
			if wPyStrip(d) != "" {
				ci.domains = append(ci.domains, strings.ToLower(strings.TrimLeft(wPyStrip(d), ".")))
			}
		}
	}
	if kind != "file" {
		fmt.Fprintln(stderr, pyDumps(newObj("error", "importing a Chrome profile needs python3 on the browser's machine")))
		return 1
	}
	cookies, err := ci.fromFile(source)
	os.Remove(source)
	if err != nil {
		var exit *cookieExit
		if !errors.As(err, &exit) {
			err = cookieExitf("could not read the cookies: " + err.Error())
		}
		fmt.Fprintln(stderr, err.Error())
		return 1
	}
	var kept []*pyObj
	for _, c := range cookies {
		domain, ok := c.Val("domain").(string)
		if !ok {
			return pyUncaught(stderr, "AttributeError", fmt.Errorf("'%s' object has no attribute 'lstrip'", pyTypeName(c.Val("domain"))))
		}
		if ci.wanted(domain) {
			kept = append(kept, c)
		}
	}
	ws, err := dialDevTools(port, wspath)
	if err != nil {
		return cookieFail(stderr, err)
	}
	defer ws.conn.Close()
	done := 0
	for i := 0; i < len(kept); i += 200 {
		batch := kept[i:min(i+200, len(kept))]
		err := ws.call("Storage.setCookies", batch)
		if err == nil {
			done += len(batch)
			continue
		}
		var exit *cookieExit
		if !errors.As(err, &exit) {
			return cookieFail(stderr, err)
		}
		for _, c := range batch {
			err := ws.call("Storage.setCookies", []*pyObj{c})
			switch {
			case err == nil:
				done++
			case errors.As(err, &exit):
				ci.skip("refused by the browser")
			default:
				return cookieFail(stderr, err)
			}
		}
	}
	sites := map[string]bool{}
	for _, c := range kept {
		sites[strings.TrimLeft(c.Str("domain"), ".")] = true
	}
	fmt.Fprintln(stdout, pyDumps(newObj("imported", done, "skipped", ci.skipped, "sites", len(sites))))
	return 0
}

// cookieFail reports an error the script did not catch (a SystemExit it
// raised, or a socket error).
func cookieFail(stderr io.Writer, err error) int {
	var exit *cookieExit
	if errors.As(err, &exit) {
		fmt.Fprintln(stderr, exit.msg)
		return 1
	}
	kind := "OSError"
	if errno, ok := pyErrno(err); ok {
		switch errno {
		case 111:
			kind = "ConnectionRefusedError"
		case 104:
			kind = "ConnectionResetError"
		case 32:
			kind = "BrokenPipeError"
		}
	} else if errors.Is(err, os.ErrDeadlineExceeded) {
		kind, err = "TimeoutError", errors.New("timed out")
	}
	return pyUncaught(stderr, kind, err)
}

func (ci *cookieImport) fromFile(path string) ([]*pyObj, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, wPyErr(err, path)
	}
	raw := wPyDecodeReplace(data)
	text := wPyStrip(raw)
	if strings.HasPrefix(text, "[") || strings.HasPrefix(text, "{") {
		return ci.fromJSON(text)
	}
	var out []*pyObj
	for _, line := range pySplitLines(raw, false) {
		httpOnly := false
		if strings.HasPrefix(line, "#HttpOnly_") {
			httpOnly, line = true, line[len("#HttpOnly_"):]
		}
		if wPyStrip(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 7 {
			ci.skip("malformed line")
			continue
		}
		var expires any
		if isDigits(f[4]) {
			n, _ := new(big.Int).SetString(f[4], 10)
			expires = n
		}
		c, err := makeCookie(f[5], f[6], f[0], f[2], expires, strings.ToUpper(f[3]) == "TRUE", httpOnly, nil)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func (ci *cookieImport) fromJSON(text string) ([]*pyObj, error) {
	data, err := pyLoads(text)
	if err != nil {
		return nil, err
	}
	items := data
	if o, ok := data.(*pyObj); ok {
		items = o.Val("cookies")
		if !o.Has("cookies") {
			items = []any{}
		}
	}
	list, err := pyIter(items)
	if err != nil {
		return nil, err
	}
	var out []*pyObj
	for _, item := range list {
		c, ok := item.(*pyObj)
		if !ok || !c.Has("name") || !c.Has("value") || !(wPyTruthy(c.Val("domain")) || wPyTruthy(c.Val("host"))) {
			ci.skip("not a cookie")
			continue
		}
		ss := c.Val("sameSite")
		if !wPyTruthy(ss) {
			ss = c.Val("same_site")
		}
		if ss != nil {
			ss = sameSites[strings.ToLower(wPyStr(ss))]
		}
		var exp any
		if v, ok := c.Get("expires"); ok {
			exp = v
		} else {
			exp = c.Val("expirationDate")
		}
		switch exp.(type) {
		case *big.Int, float64, bool:
		default:
			exp = nil
		}
		domain := c.Val("domain")
		if !wPyTruthy(domain) {
			domain = c.Val("host")
		}
		path, ok := c.Get("path")
		if !ok {
			path = "/"
		}
		cookie, err := makeCookie(c.Val("name"), wPyStr(c.Val("value")), domain, path, exp,
			wPyTruthy(c.Val("secure")), wPyTruthy(c.Val("httpOnly")), ss)
		if err != nil {
			return nil, err
		}
		out = append(out, cookie)
	}
	return out, nil
}

// makeCookie is the script's cookie(): the fields CDP's Storage.setCookies
// takes, in the same order.
func makeCookie(name, value, domain, path, expires any, secure, httpOnly bool, sameSite any) (*pyObj, error) {
	if !wPyTruthy(path) {
		path = "/"
	}
	c := newObj("name", name, "value", value, "domain", domain, "path", path, "secure", secure, "httpOnly", httpOnly)
	if n, ok := pyNumber(expires); ok && wPyTruthy(expires) && n.Sign() > 0 && !isNaN(expires) {
		f, err := pyFloatOf(expires)
		if err != nil {
			return nil, err
		}
		c.Set("expires", f)
	}
	if wPyTruthy(sameSite) {
		c.Set("sameSite", sameSite)
	}
	return c, nil
}

// pyFloatOf is float(v) for a JSON number.
func pyFloatOf(v any) (float64, error) {
	switch x := v.(type) {
	case float64:
		return x, nil
	case bool:
		if x {
			return 1, nil
		}
		return 0, nil
	case *big.Int:
		f, _ := new(big.Float).SetInt(x).Float64()
		if math.IsInf(f, 0) {
			return 0, errors.New("int too large to convert to float")
		}
		return f, nil
	}
	return 0, fmt.Errorf("must be real number, not %s", pyTypeName(v))
}

// devTools is the script's minimal websocket client.
type devTools struct {
	conn net.Conn
	r    *bufio.Reader
	next int
}

func dialDevTools(port int, path string) (*devTools, error) {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 20*time.Second)
	if err != nil {
		if errno, ok := pyErrno(err); ok {
			return nil, &pyOSError{errno: errno}
		}
		return nil, err
	}
	var nonce [16]byte
	rand.Read(nonce[:])
	key := base64.StdEncoding.EncodeToString(nonce[:])
	fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: 127.0.0.1:%d\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n", path, port, key)
	ws := &devTools{conn: conn}
	var head []byte
	buf := make([]byte, 4096)
	for !strings.Contains(string(head), "\r\n\r\n") {
		conn.SetReadDeadline(time.Now().Add(20 * time.Second))
		n, err := conn.Read(buf)
		if n == 0 && err != nil {
			if err == io.EOF {
				conn.Close()
				return nil, cookieExitf("the browser closed the DevTools connection")
			}
			conn.Close()
			return nil, err
		}
		head = append(head, buf[:n]...)
	}
	first, _, _ := strings.Cut(string(head), "\r\n")
	if !strings.Contains(first, " 101 ") {
		conn.Close()
		return nil, cookieExitf("the browser refused the DevTools connection")
	}
	_, rest, _ := strings.Cut(string(head), "\r\n\r\n")
	ws.r = bufio.NewReader(io.MultiReader(strings.NewReader(rest), conn))
	return ws, nil
}

func (ws *devTools) read(n uint64) ([]byte, error) {
	out := make([]byte, n)
	ws.conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	if _, err := io.ReadFull(ws.r, out); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return nil, cookieExitf("the browser closed the DevTools connection")
		}
		return nil, err
	}
	return out, nil
}

func (ws *devTools) send(obj *pyObj) error {
	data := []byte(pyDumps(obj))
	var mask [4]byte
	rand.Read(mask[:])
	head := []byte{0x81}
	switch n := len(data); {
	case n < 126:
		head = append(head, 0x80|byte(n))
	case n < 65536:
		head = append(head, 0x80|126)
		head = binary.BigEndian.AppendUint16(head, uint16(n))
	default:
		head = append(head, 0x80|127)
		head = binary.BigEndian.AppendUint64(head, uint64(n))
	}
	frame := append(head, mask[:]...)
	for i, b := range data {
		frame = append(frame, b^mask[i%4])
	}
	ws.conn.SetWriteDeadline(time.Now().Add(20 * time.Second))
	_, err := ws.conn.Write(frame)
	return err
}

func (ws *devTools) recv() (any, error) {
	var data []byte
	for {
		h, err := ws.read(2)
		if err != nil {
			return nil, err
		}
		n := uint64(h[1] & 0x7f)
		switch n {
		case 126:
			ext, err := ws.read(2)
			if err != nil {
				return nil, err
			}
			n = uint64(binary.BigEndian.Uint16(ext))
		case 127:
			ext, err := ws.read(8)
			if err != nil {
				return nil, err
			}
			n = binary.BigEndian.Uint64(ext)
		}
		payload, err := ws.read(n)
		if err != nil {
			return nil, err
		}
		data = append(data, payload...)
		if h[0]&0x80 != 0 {
			return pyLoads(wPyDecodeReplace(data))
		}
	}
}

func (ws *devTools) call(method string, cookies []*pyObj) error {
	ws.next++
	list := make([]any, len(cookies))
	for i, c := range cookies {
		list[i] = c
	}
	if err := ws.send(newObj("id", ws.next, "method", method, "params", newObj("cookies", list))); err != nil {
		return err
	}
	for {
		msg, err := ws.recv()
		if err != nil {
			return err
		}
		o, ok := msg.(*pyObj)
		if !ok {
			continue
		}
		if !wPyEqual(o.Val("id"), ws.next) {
			continue
		}
		if o.Has("error") {
			message := "DevTools error"
			if e, ok := o.Val("error").(*pyObj); ok && e.Has("message") {
				message = wPyStr(e.Val("message"))
			}
			return cookieExitf(message)
		}
		return nil
	}
}
