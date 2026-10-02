// Package console is a terminal client for the same API used by the web UI.
package console

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type HTTPError struct {
	Status int
	Detail string
	// Code is the server's machine-readable reason, when it sends one
	// (no_git_identity, on_base_branch…).
	Code string
}

func (e *HTTPError) Error() string { return e.Detail }

type Client struct {
	Base, Token string
	HTTP        *http.Client
}

func New(base, token string) *Client {
	return &Client{strings.TrimRight(base, "/"), token, &http.Client{Timeout: 90 * time.Second}}
}
func (c *Client) Request(method, path string, body io.Reader, contentType string) ([]byte, error) {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return nil, fmt.Errorf("API path must start with one /")
	}
	if !strings.HasPrefix(path, "/api/") {
		path = "/api" + path
	}
	u, err := url.Parse(c.Base + path)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(method, u.String(), body)
	if err != nil {
		return nil, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, (32<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 32<<20 {
		return nil, fmt.Errorf("API response exceeds 32 MiB; use download for files")
	}
	if res.StatusCode >= 300 {
		var v struct {
			Detail string `json:"detail"`
			Code   string `json:"code"`
		}
		_ = json.Unmarshal(data, &v)
		if v.Detail == "" {
			v.Detail = res.Status
		}
		return nil, &HTTPError{Status: res.StatusCode, Detail: v.Detail, Code: v.Code}
	}
	return data, nil
}
func (c *Client) JSON(method, path string, body any) ([]byte, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	return c.Request(method, path, r, "application/json")
}
func (c *Client) Upload(kind, id, path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > 25<<20 {
		return nil, fmt.Errorf("choose a regular file no larger than 25 MiB")
	}
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	part, err := w.CreateFormFile("file", filepath.Base(path))
	if err != nil {
		return nil, err
	}
	if _, err = io.Copy(part, f); err != nil {
		return nil, err
	}
	if err = w.Close(); err != nil {
		return nil, err
	}
	endpoint := "/term/" + url.PathEscape(kind) + "/" + url.PathEscape(id) + "/attachments"
	if kind == "task" {
		endpoint = "/tasks/" + url.PathEscape(id) + "/attachments"
	}
	return c.Request("POST", endpoint, &b, w.FormDataContentType())
}

// Download streams arbitrary binary files without the JSON response size limit.
// Existing local files are preserved, including when the transfer fails.
func (c *Client) Download(path, destination string) error {
	req, err := http.NewRequest("GET", c.Base+"/api"+path, nil)
	if err != nil {
		return err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("download: %s", res.Status)
	}
	f, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, res.Body)
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(destination)
	}
	return err
}
