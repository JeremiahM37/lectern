// Package redact replaces secrets in text with "[redacted]" before the text
// reaches a sink: tool results, session files and logs.
//
// Two kinds of secret are matched, in one pass each:
//
//   - shapes: well-known key prefixes (sk-ant-, sk-, ghp_, AKIA, xoxb-, AIza,
//     glpat-, ...), PEM private key blocks, bearer tokens in Authorization
//     headers and password, token, api_key and secret assignments;
//   - literal values: the values in the llm key store and any environment
//     variable whose name ends in _KEY, _TOKEN, _SECRET or _PASSWORD. Values
//     shorter than 8 characters are ignored.
//
// Shape matching is one compiled regexp scan. Literal matching is one
// strings.Replacer scan. Nothing here matches hex digests, so git SHAs and
// ordinary code pass through unchanged.
package redact

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

// Mask replaces every matched secret.
const Mask = "[redacted]"

// minLiteral is the shortest value treated as a secret literal.
const minLiteral = 8

// shape is one alternative of the combined pattern. Each alternative is a
// pair of capture groups: a kept prefix (may be empty) followed by the
// secret. ok, when set, rejects a match that looks like code rather than a
// secret.
type shape struct {
	re string
	ok func(secret string) bool
}

func hasDigit(s string) bool {
	return strings.ContainsAny(s, "0123456789")
}

// looksSecret rejects bare identifiers such as tokenFromEnv: an assignment
// value must carry a digit or a symbol that real credentials use.
func looksSecret(s string) bool {
	return strings.ContainsAny(s, "0123456789+/=!@#%^*")
}

// prefixed wraps a pattern so its whole match is the secret.
func prefixed(p string, ok func(string) bool) shape {
	return shape{re: `()(` + p + `)`, ok: ok}
}

var shapes = []shape{
	prefixed(`\bsk-ant-[A-Za-z0-9_\-]{16,}`, nil),
	prefixed(`\bsk-(?:proj-)?[A-Za-z0-9_\-]{20,}`, hasDigit),
	prefixed(`\bgh[pousr]_[A-Za-z0-9]{36,}`, nil),
	prefixed(`\bgithub_pat_[A-Za-z0-9_]{22,}`, nil),
	prefixed(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`, nil),
	prefixed(`\bxox[abp]-[A-Za-z0-9\-]{10,}`, nil),
	prefixed(`\bAIza[0-9A-Za-z_\-]{35}`, nil),
	prefixed(`\bglpat-[A-Za-z0-9_\-]{20,}`, nil),
	// PEM private key blocks, header to footer. The body may contain
	// escaped newlines when the text is JSON, which this matches as-is.
	prefixed(`-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY-----[\s\S]*?-----END (?:[A-Z0-9]+ )*PRIVATE KEY-----`, nil),
	{
		re: `((?i:authorization:\s*bearer\s+))([A-Za-z0-9._~+/=\-]{8,})`,
	},
	{
		// key, optional closing quote, separator, optional opening quote.
		// The value cannot start with = or : so `token := x` is not a hit.
		re: `((?i:(?:password|passwd|token|api[_-]?key|access[_-]?key|secret)["']?\s*[:=]\s*["']?))([A-Za-z0-9_\-][^\s"'` + "`" + `,;&()\[\]{}]{6,})`,
		ok: looksSecret,
	},
}

var (
	pattern    *regexp.Regexp
	shapeCheck []func(string) bool
)

func init() {
	parts := make([]string, len(shapes))
	shapeCheck = make([]func(string) bool, len(shapes))
	for i, s := range shapes {
		parts[i] = s.re
		shapeCheck[i] = s.ok
	}
	pattern = regexp.MustCompile(strings.Join(parts, "|"))
}

// Redactor masks a fixed set of literal values and every shape. It is
// immutable and safe for concurrent use.
type Redactor struct {
	literals []string
	lit      *strings.Replacer // nil when there are no literals
}

// New returns a Redactor for the given literal values. Values shorter than
// 8 characters are dropped. Each value is also matched in its JSON-escaped
// form, since session files store text as JSON.
func New(literals ...string) *Redactor {
	seen := map[string]bool{}
	var lits []string
	for _, v := range literals {
		if len(v) < minLiteral || seen[v] {
			continue
		}
		seen[v] = true
		lits = append(lits, v)
	}
	slices.Sort(lits)
	r := &Redactor{literals: lits}
	var pairs []string
	for _, v := range lits {
		pairs = append(pairs, v, Mask)
		if esc := jsonEscape(v); esc != v {
			pairs = append(pairs, esc, Mask)
		}
	}
	if len(pairs) > 0 {
		r.lit = strings.NewReplacer(pairs...)
	}
	return r
}

// jsonEscape returns s as it appears inside a JSON string.
func jsonEscape(s string) string {
	b, err := json.Marshal(s)
	if err != nil || len(b) < 2 {
		return s
	}
	return string(b[1 : len(b)-1])
}

// Literals returns the literal values this Redactor masks.
func (r *Redactor) Literals() []string { return slices.Clone(r.literals) }

// Redact returns s with every secret replaced by Mask. When nothing matches
// it returns s itself.
func (r *Redactor) Redact(s string) string {
	if r.lit != nil {
		s = r.lit.Replace(s)
	}
	idx := pattern.FindAllStringSubmatchIndex(s, -1)
	if len(idx) == 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	last := 0
	for _, m := range idx {
		for i := range shapes {
			// Alternative i owns groups 2i+1 (kept prefix) and 2i+2 (secret).
			start, end := m[2*(2*i+2)], m[2*(2*i+2)+1]
			if start < 0 {
				continue
			}
			if chk := shapeCheck[i]; chk != nil && !chk(s[start:end]) {
				break
			}
			b.WriteString(s[last:start])
			b.WriteString(Mask)
			last = end
			break
		}
	}
	b.WriteString(s[last:])
	return b.String()
}

// Dynamic redacts with whatever secrets are current: the key store and the
// environment are re-read on each call, and the literal set is rebuilt only
// when it changes. Keys saved during a session are masked from then on.
type Dynamic struct {
	// KeysFile is the llm key store. Empty means <config.Home()>/keys.json.
	KeysFile string
	// Env is the environment to scan. Nil means os.Environ().
	Env []string

	mu       sync.Mutex
	cur      *Redactor
	keyPath  string
	keyStamp stamp
	keyVals  []string
}

type stamp struct {
	mod  time.Time
	size int64
	ok   bool
}

var defaultDynamic = &Dynamic{}

// Default is the process-wide redactor: the key store under config.Home()
// and the process environment. Agent tool results, session files and logs
// use it.
func Default() *Dynamic { return defaultDynamic }

// Redact masks secrets in s using the current literal set.
func (d *Dynamic) Redact(s string) string {
	return d.current().Redact(s)
}

func (d *Dynamic) current() *Redactor {
	lits := d.literals()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cur == nil || !slices.Equal(d.cur.literals, sortedUnique(lits)) {
		d.cur = New(lits...)
	}
	return d.cur
}

// literals gathers the candidate secret values from the key store and the
// environment.
func (d *Dynamic) literals() []string {
	out := d.keyValues()
	env := d.Env
	if env == nil {
		env = os.Environ()
	}
	for _, kv := range env {
		name, val, ok := strings.Cut(kv, "=")
		if !ok || !secretName(name) {
			continue
		}
		out = append(out, val)
	}
	return out
}

// secretName reports whether an environment variable name ends in a
// secret-bearing suffix.
func secretName(name string) bool {
	u := strings.ToUpper(name)
	for _, suf := range []string{"_KEY", "_TOKEN", "_SECRET", "_PASSWORD"} {
		if strings.HasSuffix(u, suf) {
			return true
		}
	}
	return false
}

// keyValues returns the values of the key store, re-reading the file only
// when its size or modification time changed.
func (d *Dynamic) keyValues() []string {
	path := d.KeysFile
	if path == "" {
		path = filepath.Join(config.Home(), "keys.json")
	}
	st := statOf(path)
	d.mu.Lock()
	defer d.mu.Unlock()
	if path == d.keyPath && st == d.keyStamp {
		return d.keyVals
	}
	d.keyPath, d.keyStamp, d.keyVals = path, st, nil
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var keys map[string]string
	if json.Unmarshal(b, &keys) != nil {
		return nil
	}
	for _, v := range keys {
		d.keyVals = append(d.keyVals, v)
	}
	return d.keyVals
}

func statOf(path string) stamp {
	fi, err := os.Stat(path)
	if err != nil {
		return stamp{}
	}
	return stamp{mod: fi.ModTime(), size: fi.Size(), ok: true}
}

func sortedUnique(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range in {
		if len(v) < minLiteral || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	slices.Sort(out)
	return out
}
