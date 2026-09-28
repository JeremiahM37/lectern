package ptyhost

import (
	"reflect"
	"testing"
)

func TestFormatLanguage(t *testing.T) {
	in := Info{Name: "lec-s1", ID: 4, PID: 99, Created: 10, Activity: 20, Cwd: "/w", Current: "bash",
		Options: map[string]string{"@agentdeck-tracking-identity": "old"}}
	vars := formatVars(in)
	tracking := "#{?#{@lectern-tracking-identity},#{@lectern-tracking-identity},#{@agentdeck-tracking-identity}}"
	for _, tc := range []struct{ format, want string }{
		{"#{session_name}", "lec-s1"},
		{"#{session_created} #{session_activity}", "10 20"},
		{"#{pane_pid}|#{pane_current_command}|#{pane_current_path}", "99|bash|/w"},
		{tracking, "old"},
		{"#{==:" + tracking + ",old}", "1"},
		{"#{==:" + tracking + ",new}", "0"},
		{"#{!=:a,b}", "1"},
		{"#{nope}x", "x"},
		{"a##b", "a#b"},
		{"#{?#{extended-keys-format},#{==:#{extended-keys},off},0}", "0"},
	} {
		if got := expandFormat(tc.format, vars); got != tc.want {
			t.Errorf("%q = %q, want %q", tc.format, got, tc.want)
		}
	}
	in.Options["@lectern-tracking-identity"] = "new"
	if got := expandFormat(tracking, formatVars(in)); got != "new" {
		t.Fatalf("the new option must win: %q", got)
	}
}

func TestTargetsAndFlags(t *testing.T) {
	for _, tc := range []struct {
		t, name string
		exact   bool
	}{{"=lec-3", "lec-3", true}, {"=lec-3:", "lec-3", true}, {"lec", "lec", false}, {"a:0.1", "a", false}} {
		if name, exact := target(tc.t); name != tc.name || exact != tc.exact {
			t.Errorf("target(%q) = %q %v", tc.t, name, exact)
		}
	}
	o, err := parse([]string{"-d", "-e", "A=1", "-eB=2", "-s", "x", "-c", "/w", "--", "env", "-i"}, "scexyF")
	if err != nil || !o.flags['d'] || o.val('s') != "x" || o.val('c') != "/w" ||
		!reflect.DeepEqual(o.vals['e'], []string{"A=1", "B=2"}) || !reflect.DeepEqual(o.args, []string{"env", "-i"}) {
		t.Fatalf("parse = %+v, %v", o, err)
	}
	o, _ = parse([]string{"-p", "-t", "=a:", "-S", "-40", "-J"}, "tSEb")
	if o.val('S') != "-40" || !o.flags['J'] || !o.flags['p'] {
		t.Fatalf("capture flags = %+v", o)
	}
	words, err := splitCommand(`kill-session -t '=lec s1' "a\"b" c\ d`)
	if err != nil || !reflect.DeepEqual(words, []string{"kill-session", "-t", "=lec s1", `a"b`, "c d"}) {
		t.Fatalf("splitCommand = %q, %v", words, err)
	}
}

func TestKeyNames(t *testing.T) {
	for _, tc := range []struct {
		name string
		app  bool
		want string
	}{{"Enter", false, "\r"}, {"Escape", false, "\x1b"}, {"C-c", false, "\x03"}, {"Up", false, "\x1b[A"},
		{"Up", true, "\x1bOA"}, {"Tab", false, "\t"}, {"M-x", false, "\x1bx"}, {"hello", false, "hello"}} {
		if got, _ := keyBytes(tc.name, tc.app); string(got) != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestOSCDirectory(t *testing.T) {
	if got := oscDir("file://host/home/me/x"); got != "/home/me/x" {
		t.Fatalf("oscDir = %q", got)
	}
}
