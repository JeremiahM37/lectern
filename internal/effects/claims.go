// Package effects checks the claims an agent makes about the world ("pushed",
// "restarted the service", "it is live at …") by probing the effect itself.
//
// A pilot on real sessions showed that the false completions a human catches
// are mostly effect claims that were only proxy-verified: a test ran, and then
// the agent said "pushed". Extract finds those claims in the agent's final
// message, deterministically and without a model. Probe (probe.go) then looks
// at the machine with read-only commands. The package never changes state.
package effects

import (
	"regexp"
	"strings"
)

// Kind names what a completion claim asserts.
type Kind string

const (
	KindPushed        Kind = "pushed"
	KindCommitted     Kind = "committed"
	KindTestsPass     Kind = "tests_pass"
	KindRestarted     Kind = "restarted"
	KindURLOK         Kind = "url_ok"
	KindPortListening Kind = "port_listening"
	KindFileExists    Kind = "file_exists"
)

// Claim is one completion claim found in a message.
type Claim struct {
	Kind Kind
	// Target is what the claim is about: a unit name, URL, port, path, commit
	// or remote ref. It is empty when the sentence names none.
	Target string
	// Sentence is the sentence the claim was found in, for evidence.
	Sentence string
}

// sentenceEnd matches terminal punctuation followed by whitespace or the end
// of the line. "lectern.service" and "v2.8.1" are not sentence ends.
var sentenceEnd = regexp.MustCompile(`([.!?]+)(\s+|$)`)

// splitSentences breaks a message into sentences. Newlines always end a
// sentence, so bullet points are separate claims.
func splitSentences(message string) []string {
	var out []string
	for _, line := range strings.Split(message, "\n") {
		start := 0
		for _, m := range sentenceEnd.FindAllStringSubmatchIndex(line, -1) {
			out = appendSentence(out, line[start:m[3]])
			start = m[1]
		}
		out = appendSentence(out, line[start:])
	}
	return out
}

func appendSentence(out []string, s string) []string {
	if t := strings.TrimSpace(s); t != "" {
		return append(out, t)
	}
	return out
}

// Ignore rules. A sentence that is a question, future or conditional, or
// negated makes no completion claim, so no kind is checked for it.
var (
	questionStart = regexp.MustCompile(`(?i)^[\W_]*(should|shall|do|does|did|is|are|was|were|can|could|would|will|may|might|what|why|how|when|where|who|which)\b`)
	negation      = regexp.MustCompile(`(?i)\b(not|no|never|none|nothing|without|cannot|unable|yet|neither|nor|nope)\b|n't\b`)
	futureOrCond  = regexp.MustCompile(`(?i)\b(you can|you may|i'll|i will|i'm going|i am going|i'm about|i am about|going to|gonna|will|would|should|could|might|may|once|after you|when you|if|unless|let me|let's|need to|needs to|want to|plan to|about to|todo|to be|before|until|to push|to restart|to deploy|to commit|to merge|to run|to verify|to check)\b`)
)

// claimable reports whether a sentence can carry a completion claim at all.
func claimable(s string) bool {
	if strings.HasSuffix(strings.TrimSpace(s), "?") {
		return false
	}
	if questionStart.MatchString(s) {
		return false
	}
	return !negation.MatchString(s) && !futureOrCond.MatchString(s)
}

// Extract returns every completion claim in the message, in sentence order.
// The same claim may appear more than once; Evaluate de-duplicates.
func Extract(message string) []Claim {
	var out []Claim
	for _, s := range splitSentences(message) {
		if !claimable(s) {
			continue
		}
		out = append(out, claimsIn(s)...)
	}
	return out
}

var (
	pushedRe    = regexp.MustCompile(`(?i)\bpushed\b|\b(is|are|now|it's) (on|in|to) (the )?(remote|origin/\S+|origin|main|master)\b`)
	pushedRef   = regexp.MustCompile(`(?i)\borigin/[\w./-]+`)
	committedRe = regexp.MustCompile(`(?i)\bcommitted\b|\bcommit [0-9a-f]{7,40}\b`)
	committedSh = regexp.MustCompile(`(?i)\b([0-9a-f]{7,40})\b`)
	testsRe     = regexp.MustCompile(`(?i)\btests?\s+(?:are\s+|is\s+|now\s+|all\s+)*(?:pass|passes|passed|passing|green)\b|\btests?\s+(?:are|is)\s+green\b|\bgo test\b.{0,40}?\b(?:passes|passed|green)\b|(?i:\bverify\b)[^.]{0,20}?\bPASS\b|(?i:\bverify\b)\s+(?:passes|passed)\b`)
	restartedRe = regexp.MustCompile(`(?i)\brestarted\b|\bdeployed\b|\bservice is running (the )?new build\b|\brunning the new build\b`)
	unitRe      = regexp.MustCompile(`(?i)\b([\w@.-]+\.service)\b`)
	restartedNm = regexp.MustCompile(`(?i)\brestarted\s+([\w@.-]+)`)
	urlRe       = regexp.MustCompile(`https?://[^\s<>"')\]]+`)
	urlLive     = regexp.MustCompile(`(?i)\b(live|works|working|serving|reachable|returns|responds|loads)\b|\bis up\b`)
	portRe      = regexp.MustCompile(`(?i)\blisten(?:ing|s)? on (?:port\s+|\S*?:)(\d{1,5})\b|\bport (\d{1,5}) (?:is |now )?(?:up|open|listening|live|reachable)\b`)
	fileRe      = regexp.MustCompile(`(?i)\b(?:created|wrote|saved|added)\s+(?:(?:the|a|an|new)\s+)?(?:file\s+)?(?:to\s+)?[` + "`" + `'"]?([\w@+~./-]+)`)
)

// stopUnits are words that follow "restarted" without naming a unit.
var stopUnits = map[string]bool{"the": true, "service": true, "it": true, "them": true, "all": true, "a": true, "an": true, "this": true, "that": true}

func claimsIn(s string) []Claim {
	var out []Claim
	add := func(k Kind, target string) {
		out = append(out, Claim{Kind: k, Target: target, Sentence: s})
	}
	if pushedRe.MatchString(s) {
		add(KindPushed, strings.TrimRight(pushedRef.FindString(s), ".,;:"))
	}
	if committedRe.MatchString(s) {
		target := ""
		// A commit id must contain a digit, so the word "defaced" is not one.
		for _, m := range committedSh.FindAllStringSubmatch(s, -1) {
			if strings.ContainsAny(m[1], "0123456789") {
				target = m[1]
				break
			}
		}
		add(KindCommitted, target)
	}
	if testsRe.MatchString(s) {
		add(KindTestsPass, "")
	}
	if restartedRe.MatchString(s) {
		target := ""
		if m := unitRe.FindStringSubmatch(s); m != nil {
			target = m[1]
		} else if m := restartedNm.FindStringSubmatch(s); m != nil {
			if name := strings.TrimRight(m[1], "."); !stopUnits[strings.ToLower(name)] {
				target = name
			}
		}
		add(KindRestarted, target)
	}
	if u := urlRe.FindString(s); u != "" && urlLive.MatchString(s) {
		add(KindURLOK, strings.TrimRight(u, ".,;:!?`"))
	}
	if m := portRe.FindStringSubmatch(s); m != nil {
		port := m[1]
		if port == "" {
			port = m[2]
		}
		if validPort(port) {
			add(KindPortListening, port)
		}
	}
	for _, m := range fileRe.FindAllStringSubmatch(s, -1) {
		if p := cleanPath(m[1]); p != "" {
			add(KindFileExists, p)
		}
	}
	return out
}

// validPort accepts 1..65535 without a leading zero.
func validPort(p string) bool {
	if p == "" || p[0] == '0' || len(p) > 5 {
		return false
	}
	n := 0
	for _, c := range p {
		n = n*10 + int(c-'0')
	}
	return n >= 1 && n <= 65535
}

// cleanPath keeps only tokens that look like paths: they need a separator or a
// dot, so "added a test" or "wrote to disk" make no file claim.
func cleanPath(p string) string {
	p = strings.TrimRight(p, ".,;:")
	if p == "" || !strings.ContainsAny(p, "/.") || strings.Trim(p, "./~") == "" {
		return ""
	}
	return p
}

// dedupe keeps the first claim for each kind and target.
func dedupe(claims []Claim) []Claim {
	seen := map[string]bool{}
	var out []Claim
	for _, c := range claims {
		key := string(c.Kind) + "\x00" + c.Target
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, c)
	}
	return out
}
