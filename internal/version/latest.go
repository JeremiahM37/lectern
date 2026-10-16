package version

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// LatestTag asks a "latest release" endpoint for the tag of the newest
// release. The GitHub releases API and a mirror serving the same JSON shape
// (tag_name) both work. The response is capped at 1 MiB.
func LatestTag(ctx context.Context, client *http.Client, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "lectern-version/"+Version)
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: %s", url, res.Status)
	}
	var release struct {
		Tag string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&release); err != nil {
		return "", err
	}
	if release.Tag == "" {
		return "", errors.New("the release list had no tag")
	}
	return release.Tag, nil
}
