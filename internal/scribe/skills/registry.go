package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// DefaultRegistryURL is the public skill registry (skills.sh). Its search
// endpoint answers {skills: [...], count} for a query.
const DefaultRegistryURL = "https://skills.sh"

// popularQuery is the search that stands in for "popular skills".
const popularQuery = "ai"

// registryTimeout bounds one registry request. The browser is interactive,
// so a slow registry is treated like an offline one.
const registryTimeout = 8 * time.Second

// maxRegistryBody caps what is read from the registry.
const maxRegistryBody = 1 << 20

// RegistryEntry is one skill the registry lists.
type RegistryEntry struct {
	ID       string // what the registry calls the entry, e.g. "owner/repo/review"
	SkillID  string // the skill's directory name in its repository
	Name     string
	Source   string // GitHub repository, "owner/repo"
	Installs int
}

// InstallSource is the value Install takes for this entry: the repository's
// clone URL with "#<skill>" to pick this skill out of it.
func (e RegistryEntry) InstallSource() string {
	return "https://github.com/" + e.Source + ".git#" + e.SkillID
}

// Registry is a client for the skill registry. The zero value is not usable;
// use NewRegistry.
type Registry struct {
	BaseURL string
	Client  *http.Client
}

// NewRegistry returns a client for the public registry.
func NewRegistry() *Registry {
	return &Registry{BaseURL: DefaultRegistryURL, Client: &http.Client{Timeout: registryTimeout}}
}

// ErrRegistryUnavailable wraps every failure to reach or read the registry,
// so callers can degrade quietly when offline.
var ErrRegistryUnavailable = errors.New("skill registry unavailable")

// Popular lists the registry's popular skills (a search for "ai").
func (r *Registry) Popular(ctx context.Context) ([]RegistryEntry, error) {
	return r.Search(ctx, popularQuery)
}

// Search lists the skills matching q.
func (r *Registry) Search(ctx context.Context, q string) ([]RegistryEntry, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, errors.New("search needs a query")
	}
	if r.BaseURL == "" {
		return nil, fmt.Errorf("%w: no registry URL", ErrRegistryUnavailable)
	}
	u, err := url.Parse(strings.TrimRight(r.BaseURL, "/") + "/api/search")
	if err != nil {
		return nil, err
	}
	v := u.Query()
	v.Set("q", q)
	u.RawQuery = v.Encode()

	ctx, cancel := context.WithTimeout(ctx, registryTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "scribe-skills")
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: registryTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRegistryUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: registry answered %s", ErrRegistryUnavailable, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRegistryBody+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRegistryUnavailable, err)
	}
	if len(body) > maxRegistryBody {
		return nil, fmt.Errorf("%w: response is larger than %d bytes", ErrRegistryUnavailable, maxRegistryBody)
	}
	return parseRegistry(body)
}

// registryWire is the registry's search response.
type registryWire struct {
	Skills []struct {
		ID       string `json:"id"`
		SkillID  string `json:"skillId"`
		Name     string `json:"name"`
		Installs int    `json:"installs"`
		Source   string `json:"source"`
	} `json:"skills"`
}

var (
	// repoRe matches "owner/repo" as GitHub allows it. Anything else is
	// dropped, so an entry can only ever point at a GitHub repository.
	repoRe = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)
	// skillRe matches a skill directory name.
	skillRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// parseRegistry decodes a search response. Entries that cannot be installed
// (no skill id, or a source that is not owner/repo) are skipped rather than
// failing the whole list.
func parseRegistry(body []byte) ([]RegistryEntry, error) {
	var w registryWire
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, fmt.Errorf("%w: unreadable response: %v", ErrRegistryUnavailable, err)
	}
	out := make([]RegistryEntry, 0, len(w.Skills))
	for _, s := range w.Skills {
		skillID := strings.TrimSpace(s.SkillID)
		source := strings.Trim(strings.TrimSpace(s.Source), "/")
		if !skillRe.MatchString(skillID) || !repoRe.MatchString(source) || strings.HasPrefix(skillID, ".") {
			continue
		}
		id := strings.TrimSpace(s.ID)
		if id == "" {
			id = source + "/" + skillID
		}
		out = append(out, RegistryEntry{ID: id, SkillID: skillID, Name: s.Name, Source: source, Installs: max(0, s.Installs)})
	}
	return out, nil
}
