package replay

import "encoding/json"

// ghPR mirrors exactly the fields the import pipeline requests:
// `gh pr list --json number,title,body,mergeCommit,baseRefName,files,closingIssuesReferences`.
type ghPR struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	MergeCommit struct {
		OID string `json:"oid"`
	} `json:"mergeCommit"`
	BaseRefName string `json:"baseRefName"`
	Files       []struct {
		Path      string `json:"path"`
		Additions int    `json:"additions"`
		Deletions int    `json:"deletions"`
	} `json:"files"`
	ClosingIssuesReferences []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
	} `json:"closingIssuesReferences"`
}

// ParseGHPRList parses `gh pr list --json ...`'s stdout into PRs. A row with
// no merge commit oid is dropped rather than surfaced as a confusing
// candidate — it cannot give us a base_ref (the merge's first parent), which
// every replay case needs, and gh can return such a row in edge cases (e.g.
// a merge queue rewrite between listing and this query).
func ParseGHPRList(raw []byte) ([]PR, error) {
	var rows []ghPR
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	out := make([]PR, 0, len(rows))
	for _, r := range rows {
		if r.MergeCommit.OID == "" {
			continue
		}
		pr := PR{
			Number: r.Number, Title: r.Title, Body: r.Body,
			MergeSHA: r.MergeCommit.OID, BaseRefName: r.BaseRefName,
		}
		for _, f := range r.Files {
			pr.Files = append(pr.Files, FileStat{Path: f.Path, Additions: f.Additions, Deletions: f.Deletions})
		}
		for _, iss := range r.ClosingIssuesReferences {
			pr.Issues = append(pr.Issues, Issue{Number: iss.Number, Title: iss.Title, Body: iss.Body})
		}
		out = append(out, pr)
	}
	return out, nil
}
