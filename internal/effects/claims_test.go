package effects

import (
	"sort"
	"strings"
	"testing"
)

// The sentences are synthetic. Each row is one message and the claims it
// should yield, written as "kind" or "kind=target". Ignore rows want none.
func TestExtractTable(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want []string
	}{
		// pushed
		{"pushed branch", "I pushed the branch to origin/main.", []string{"pushed=origin/main"}},
		{"pushed short", "Pushed to origin/main.", []string{"pushed=origin/main"}},
		{"on main", "The fix is on main now.", []string{"pushed"}},
		{"on remote", "The commit is on the remote.", []string{"pushed"}},
		// committed
		{"committed", "I committed the changes.", []string{"committed"}},
		{"commit sha", "commit abc1234 is in the log.", []string{"committed=abc1234"}},
		{"committed as sha", "Committed as 1a2b3c4d on the feature branch.", []string{"committed=1a2b3c4d"}},
		// tests_pass
		{"all tests pass", "All tests pass.", []string{"tests_pass"}},
		{"tests green", "The tests are green.", []string{"tests_pass"}},
		{"go test passes", "go test ./internal/foo passes.", []string{"tests_pass"}},
		{"verify PASS", "verify PASS", []string{"tests_pass"}},
		// restarted
		{"restarted unit", "Restarted lectern.service, and it is running the new build.", []string{"restarted=lectern.service"}},
		{"restarted name", "Restarted foo.", []string{"restarted=foo"}},
		{"restarted the service", "I restarted the service.", []string{"restarted"}},
		{"deployed", "Deployed the build.", []string{"restarted"}},
		// url_ok
		{"url live", "The app is live at https://example.test/app now.", []string{"url_ok=https://example.test/app"}},
		{"url returns", "http://localhost:9110/ returns 200.", []string{"url_ok=http://localhost:9110/"}},
		{"url trailing dot", "The dashboard works at https://dash.example.org.", []string{"url_ok=https://dash.example.org"}},
		{"url reachable", "It is reachable at http://10.0.0.5:8080.", []string{"url_ok=http://10.0.0.5:8080"}},
		// port_listening
		{"listening colon", "Listening on :8080 now.", []string{"port_listening=8080"}},
		{"port is up", "The port 9110 is up.", []string{"port_listening=9110"}},
		{"port is listening", "Port 443 is listening.", []string{"port_listening=443"}},
		{"listening with host", "Listening on 127.0.0.1:7000.", []string{"port_listening=7000"}},
		// file_exists
		{"wrote backtick", "Wrote `docs/new.md`.", []string{"file_exists=docs/new.md"}},
		{"created the file", "Created the file config/app.yaml.", []string{"file_exists=config/app.yaml"}},
		{"added backtick", "Added `scripts/run.sh` to the repo.", []string{"file_exists=scripts/run.sh"}},
		{"saved absolute", "Saved /tmp/out.txt.", []string{"file_exists=/tmp/out.txt"}},
		{"created a PR", "Created a PR for the branch.", nil},
		{"wrote to disk", "Wrote to disk.", nil},
		{"version is not a path", "The release is v2.8.1 and it is shipped.", nil},

		// Ignore: questions
		{"question mark", "Should I restart lectern.service?", nil},
		{"did you push", "Did you push to origin/main?", nil},
		{"would you like", "Would you like me to commit?", nil},
		{"question start no mark", "Is the service running the new build", nil},
		{"file question", "Did the file get saved to /tmp/x.txt?", nil},

		// Ignore: future and conditional
		{"ill push", "I'll push the branch once the tests pass.", nil},
		{"once you push", "Once you push, the service will restart.", nil},
		{"if tests pass", "If the tests pass, I will push.", nil},
		{"tests will pass", "Tests will pass after the restart.", nil},
		{"need to restart", "I need to restart lectern.service.", nil},
		{"let me push", "Let me push this to origin/main.", nil},
		{"next ill restart", "Next I'll restart the service.", nil},
		{"before pushing", "Before pushing, check the tests pass.", nil},
		{"would be live", "The fix would be live at https://x.test if deployed.", nil},
		{"unless", "Created ~/notes.md, unless it already exists.", nil},
		{"is up if", "The URL https://x.test is up if you want.", nil},
		{"you can run", "You can run go test ./... to confirm.", nil},
		{"commit imperative", "Commit the change when you are happy.", nil},
		{"restart imperative", "Restart the service when ready.", nil},

		// Ignore: negated
		{"not pushed yet", "The branch is not pushed yet.", nil},
		{"have not committed", "I have not committed these changes.", nil},
		{"tests did not pass", "Tests did not pass on the last run.", nil},
		{"nothing committed", "Nothing has been committed.", nil},
		{"not listening", "The service is not listening on :8080.", nil},
		{"cannot confirm", "I cannot confirm the URL works.", nil},
		{"does not return", "The URL https://x.test does not return 200.", nil},
		{"port not up", "Port 9110 is not up.", nil},
		{"pushing failed", "Pushing failed, so nothing is on origin/main.", nil},
		{"do not restart", "Do not restart the service yet.", nil},
		{"havent run", "Tests pass, but I haven't run them on this tree.", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := describe(Extract(tc.msg))
			want := append([]string(nil), tc.want...)
			sort.Strings(want)
			if strings.Join(got, "|") != strings.Join(want, "|") {
				t.Fatalf("Extract(%q)\n got %v\nwant %v", tc.msg, got, want)
			}
		})
	}
}

func describe(claims []Claim) []string {
	out := []string{}
	for _, c := range claims {
		s := string(c.Kind)
		if c.Target != "" {
			s += "=" + c.Target
		}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func TestSentenceSplitKeepsDottedNames(t *testing.T) {
	got := splitSentences("Upgraded to v2.8.1 on lectern.service. Pushed it.\nRestarted foo.")
	want := []string{"Upgraded to v2.8.1 on lectern.service.", "Pushed it.", "Restarted foo."}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("splitSentences: got %q want %q", got, want)
	}
}

func TestExtractDeduplicatesOnlyInEvaluate(t *testing.T) {
	msg := "Pushed to origin/main. Pushed to origin/main again."
	if n := len(Extract(msg)); n != 2 {
		t.Fatalf("Extract should keep repeats, got %d claims", n)
	}
	if n := len(dedupe(Extract(msg))); n != 1 {
		t.Fatalf("dedupe should keep one pushed claim, got %d", n)
	}
}
