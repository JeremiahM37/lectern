package helpers

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func init() { Register("demo-agent", demoAgent) }

func demoAgent(_ []string, in io.Reader, out, errOut io.Writer) int {
	fmt.Fprintln(out, "Demo agent: no AI or account needed. Type a message and press Enter.")
	fmt.Fprintln(out, "Before the first edit, Lectern will ask for approval. Then open Review & merge to see your change.")
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	asked := false
	for {
		fmt.Fprint(out, "> ")
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		demoNotify("UserPromptSubmit")
		if !asked && os.Getenv("LECTERN_DEMO_ASK") == "1" {
			fmt.Fprintln(out, "Needs you: allow writing demo-notes.md in Lectern's approval card.")
			allowed, err := demoPermission()
			asked = true
			if err != nil {
				fmt.Fprintln(out, "Could not confirm approval; no file was written. Try again after checking the connection.")
				asked = false
				demoNotify("Stop")
				continue
			}
			if !allowed {
				fmt.Fprintln(out, "Denied: no file was written. You can send another message to try again.")
				asked = false
				demoNotify("Stop")
				continue
			}
		}
		f, err := os.OpenFile("demo-notes.md", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			fmt.Fprintln(errOut, err)
			continue
		}
		_, err = fmt.Fprintf(f, "- %s\n", line)
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			fmt.Fprintln(errOut, "Could not write demo-notes.md")
			continue
		}
		demoNotify("PostToolUse")
		demoNotify("Stop")
		fmt.Fprintln(out, "Done: I added that to demo-notes.md. Open Review & merge to see the change.")
	}
	demoNotify("SessionEnd")
	if err := scanner.Err(); err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	return 0
}

func demoPermission() (bool, error) {
	base := strings.TrimRight(os.Getenv("LECTERN_HOOK_URL"), "/")
	token := os.Getenv("LECTERN_HOOK_TOKEN")
	if base == "" || token == "" {
		return false, fmt.Errorf("approval connection missing")
	}
	req, err := http.NewRequest("POST", base+"/PermissionRequest", bytes.NewBufferString(`{"tool_name":"Write","tool_input":{"file_path":"demo-notes.md","content":"Your demo message"}}`))
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 125 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return false, fmt.Errorf("approval status %d", res.StatusCode)
	}
	var body struct {
		Output struct {
			Decision struct {
				Behavior string `json:"behavior"`
			} `json:"decision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&body); err != nil {
		return false, err
	}
	return body.Output.Decision.Behavior == "allow", nil
}

// Report lifecycle changes through the same hooks real coding agents use.
func demoNotify(event string) {
	base := strings.TrimRight(os.Getenv("LECTERN_HOOK_URL"), "/")
	token := os.Getenv("LECTERN_HOOK_TOKEN")
	if base == "" || token == "" {
		return
	}
	req, err := http.NewRequest("POST", base+"/"+event, strings.NewReader(`{"tool_name":"Write","tool_input":{"file_path":"demo-notes.md"}}`))
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if response, err := client.Do(req); err == nil {
		response.Body.Close()
	}
}
