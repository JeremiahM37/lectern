package redact

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fake credentials. They have the right shape and are not live keys.
const (
	fakeAnthropic = "sk-ant-api03-FAKEFAKEFAKEFAKEFAKE1234"
	fakeOpenAI    = "sk-proj-FAKE0123456789ABCDEFGHIJ"
	fakeGitHub    = "ghp_FAKE0123456789abcdefghijklmnopqrstuv"
	fakeFinePAT   = "github_pat_FAKE0123456789_abcdefghijklmn"
	fakeAWS       = "AKIAFAKE0123456789AB"
	fakeSlack     = "xoxb-FAKE-0123456789-abcdefghij"
	fakeGoogle    = "AIzaFAKE0123456789abcdefghijklmnopqrstu"
	fakeGitLab    = "glpat-FAKE0123456789abcdefgh"
)

func TestShapesAreMasked(t *testing.T) {
	r := New()
	cases := map[string]string{
		"anthropic":  fakeAnthropic,
		"openai":     fakeOpenAI,
		"github":     fakeGitHub,
		"fine-grain": fakeFinePAT,
		"aws":        fakeAWS,
		"slack":      fakeSlack,
		"google":     fakeGoogle,
		"gitlab":     fakeGitLab,
	}
	for name, key := range cases {
		t.Run(name, func(t *testing.T) {
			in := "result: " + key + " end"
			got := r.Redact(in)
			if strings.Contains(got, key) {
				t.Fatalf("raw key survived: %q", got)
			}
			want := "result: " + Mask + " end"
			if got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		})
	}
}

func TestNewShapesAreMasked(t *testing.T) {
	// Fake values with the right shape only.
	cases := map[string]string{
		"jwt":         "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJGQUtFMTIzIn0.SflKxwRJSMeKKF2QT4",
		"stripe-sk":   "sk_live_FAKE0123456789abcdefgh",
		"stripe-test": "sk_test_FAKE0123456789abcdefgh",
		"stripe-rk":   "rk_live_FAKE0123456789abcdefgh",
		"stripe-wh":   "whsec_FAKE0123456789abcdefgh",
		"npm":         "npm_FAKE0123456789abcdefghijklmnopqrstu",
	}
	for name, key := range cases {
		t.Run(name, func(t *testing.T) {
			got := New().Redact("token is " + key + " end")
			if strings.Contains(got, key) {
				t.Fatalf("raw secret survived: %q", got)
			}
			if !strings.HasSuffix(got, " end") {
				t.Fatalf("surrounding text damaged: %q", got)
			}
		})
	}
}

func TestURLUserinfoKeepsUserAndHost(t *testing.T) {
	r := New()
	cases := []struct{ in, want string }{
		{"postgres://app:Sup3rS3cret@db.internal:5432/main", "postgres://app:[redacted]@db.internal:5432/main"},
		{"mysql://root:hunter2hunter2@127.0.0.1/shop", "mysql://root:[redacted]@127.0.0.1/shop"},
		{"mongodb+srv://svc:p4ssw0rd-xyz@cluster0.example.net/?retryWrites=true", "mongodb+srv://svc:[redacted]@cluster0.example.net/?retryWrites=true"},
		{"redis://:r3disPassw0rd@cache:6379", "redis://:[redacted]@cache:6379"},
		{"https://bot:tok3nvalue99@git.example.com/repo.git", "https://bot:[redacted]@git.example.com/repo.git"},
		{`{"url":"postgres://app:Sup3rS3cret@db/x"}`, `{"url":"postgres://app:[redacted]@db/x"}`},
	}
	for _, c := range cases {
		if got := r.Redact(c.in); got != c.want {
			t.Errorf("Redact(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

// Code and output that look near the new shapes must pass through untouched.
func TestNewShapesNoFalsePositives(t *testing.T) {
	ok := []string{
		`git clone git@github.com:JeremiahM37/lectern.git`,
		`ssh://git@github.com/org/repo`,
		`listen on https://example.com:8443/path`,
		`url := "postgres://${DB_USER}:${DB_PASS}@db:5432/app"`,
		`dsn: postgres://app:<password>@localhost/app`,
		`see https://docs.example.com:443/guide and then stop`,
		`export npm_config_cache=/home/admin/.npm npm_package_version`,
		`npm install && npm run build`,
		`stripe: sk_test_placeholder and pk_live_publishable00000000`,
		`const kind = "eyJ"`,
		`header.payload.sig in prose eyJ is base64 for {"`,
		`whsec_ prefix only`,
	}
	r := New()
	for _, in := range ok {
		if got := r.Redact(in); got != in {
			t.Errorf("ordinary text changed:\n%s", diff(in, got))
		}
	}
}

func TestPEMBlockMasked(t *testing.T) {
	pem := "-----BEGIN RSA PRIVATE KEY-----\nMIIEFAKEBODY0123\nABCDEF\n-----END RSA PRIVATE KEY-----"
	got := New().Redact("cat key.pem:\n" + pem + "\ndone")
	if strings.Contains(got, "MIIEFAKE") || strings.Contains(got, "BEGIN RSA") {
		t.Fatalf("PEM body survived: %q", got)
	}
	if got != "cat key.pem:\n"+Mask+"\ndone" {
		t.Fatalf("unexpected output: %q", got)
	}
	// The same block as it appears inside a JSON string.
	jsonForm := strings.ReplaceAll(pem, "\n", `\n`)
	got = New().Redact(`{"text":"` + jsonForm + `"}`)
	if strings.Contains(got, "MIIEFAKE") {
		t.Fatalf("JSON-escaped PEM body survived: %q", got)
	}
}

func TestBearerAndAssignmentsKeepTheLabel(t *testing.T) {
	r := New()
	cases := []struct{ in, want string }{
		{`curl -H "Authorization: Bearer abc123def456ghi" https://x`, `curl -H "Authorization: Bearer [redacted]" https://x`},
		{"authorization: bearer ZZZZ9999yyyy", "authorization: bearer [redacted]"},
		{"password=hunter22x", "password=[redacted]"},
		{`password: "hunter22x"`, `password: "[redacted]"`},
		{`{"api_key":"Zk91ffTT_2a"}`, `{"api_key":"[redacted]"}`},
		{"TOKEN=s3cr3t-value-99", "TOKEN=[redacted]"},
	}
	for _, c := range cases {
		if got := r.Redact(c.in); got != c.want {
			t.Errorf("Redact(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

// Ordinary code and git output must pass through untouched.
func TestNoFalsePositives(t *testing.T) {
	const goFile = `package main

import (
	"fmt"
	"os"
	"strings"
)

type Tokenizer struct{ count int }

func tokenFromEnv() string { return os.Getenv("HOME") }

func main() {
	token := tokenFromEnv()
	secretKey := os.Getenv("SECRET_KEY_NAME")
	password = strings.TrimSpace(input)
	apiKeyFor(token)
	tokens := []string{"alpha", "beta"}
	fmt.Println(token, secretKey, tokens, disk-usage, task-manager-service)
	fmt.Println("looks like a secret: " + strings.Join(c.secrets, ", "))
	// sk-learn style names and dates are not keys either.
	fmt.Println("2026-10-16", "sk-learn", "risk-assessment-tool")
}
`
	const gitOut = `commit 3f1a9c0d7e8b6a5f4c3d2e1b0a9f8e7d6c5b4a39
Merge: 1a28248f 57caefaa
    fix(scribe): redact tokens in ui records 0123456789abcdef0123456789abcdef01234567
    short sha 1a28248f and 57caefaa, tree 9e107d9d372bb6826bd81d3542a419d6
`
	r := New()
	for name, in := range map[string]string{"go": goFile, "git": gitOut} {
		if got := r.Redact(in); got != in {
			t.Errorf("%s: ordinary text changed:\n%s", name, diff(in, got))
		}
	}
}

func TestLiteralValues(t *testing.T) {
	r := New("supersecretvalue", "abc", `quo"te-secret`)
	if got := r.Redact("v=supersecretvalue ok"); got != "v=[redacted] ok" {
		t.Errorf("literal not masked: %q", got)
	}
	if got := r.Redact("abc is short"); got != "abc is short" {
		t.Errorf("value under 8 chars was masked: %q", got)
	}
	// Inside JSON the quote is escaped; the escaped form must be masked too.
	if got := r.Redact(`{"k":"quo\"te-secret"}`); strings.Contains(got, "quo") {
		t.Errorf("JSON-escaped literal survived: %q", got)
	}
}

func TestDynamicReadsKeysAndEnv(t *testing.T) {
	dir := t.TempDir()
	keys := filepath.Join(dir, "keys.json")
	d := &Dynamic{KeysFile: keys, Env: []string{
		"PATH=/usr/bin",
		"MY_SERVICE_TOKEN=envtoken-12345678",
		"SHORT_KEY=abc",
		"HOME_TOKENIZER=notasecretname",
	}}

	if got := d.Redact("a envtoken-12345678 b"); got != "a [redacted] b" {
		t.Errorf("env token not masked: %q", got)
	}
	if got := d.Redact("SHORT_KEY=abc"); got != "SHORT_KEY=abc" {
		t.Errorf("short env value masked: %q", got)
	}

	// A key saved after the redactor was built is masked on the next call.
	if got := d.Redact("used keyvalue-998877"); got != "used keyvalue-998877" {
		t.Fatalf("precondition: key not yet stored, got %q", got)
	}
	if err := os.WriteFile(keys, []byte(`{"anthropic":"keyvalue-998877"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := d.Redact("used keyvalue-998877"); got != "used [redacted]" {
		t.Errorf("stored key not masked after save: %q", got)
	}
}

func TestDynamicMissingKeysFile(t *testing.T) {
	d := &Dynamic{KeysFile: filepath.Join(t.TempDir(), "absent.json"), Env: nil}
	in := "plain text with no secrets"
	if got := d.Redact(in); got != in {
		t.Errorf("changed plain text: %q", got)
	}
}

func diff(a, b string) string {
	return "want:\n" + a + "\ngot:\n" + b
}

// A key that follows a JSON escape (\n in the file is backslash-n) must be
// masked: \b fails there because n is a word character. Session files hold
// text in exactly that form.
func TestKeyAfterJSONEscapeIsMasked(t *testing.T) {
	r := New()
	for _, in := range []string{
		`{"text":"output:\n` + fakeAnthropic + `\nend"}`,
		`{"text":"tab\t` + fakeGitHub + `"}`,
		`{"text":"cr\r` + fakeOpenAI + `"}`,
	} {
		got := r.Redact(in)
		for _, key := range []string{fakeAnthropic, fakeGitHub, fakeOpenAI} {
			if strings.Contains(got, key) {
				t.Fatalf("raw key survived after an escape: %s", got)
			}
		}
		if !strings.Contains(got, Mask) {
			t.Fatalf("nothing masked: %s", got)
		}
	}
	// A key glued to a preceding word is not a separate secret, as before.
	if got := r.Redact("xs" + fakeAnthropic); got != "xs"+fakeAnthropic {
		t.Fatalf("glued prefix changed: %q", got)
	}
}
