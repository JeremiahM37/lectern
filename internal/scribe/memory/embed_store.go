package memory

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

const (
	// backfillLimit bounds how many rows one backfill run re-embeds. The rest
	// wait for the next run, so activating a model never runs unbounded.
	backfillLimit = 200
	// backfillBatch is how many texts go in one provider request.
	backfillBatch = 16
	// embedTimeout bounds one background embed of a record.
	embedTimeout = 30 * time.Second
	// queryEmbedTimeout bounds the embed of a query during recall. Recall
	// must stay responsive, so a slow provider falls back to the hash.
	queryEmbedTimeout = 3 * time.Second
	// activationTimeout bounds the smoke test when a model is switched on.
	activationTimeout = 10 * time.Second
)

// pendingEmbed is a row waiting for a provider vector. updated is the
// version it was read at, so a vector computed from stale text is not stored
// over a newer edit.
type pendingEmbed struct {
	id      string
	text    string
	updated string
}

// SetEmbedder turns on a provider embedder for both stores, or turns it off
// when e is nil. It smoke-tests the provider first, and on failure keeps the
// hash embedding and returns the error. Switching on the same model again is
// a no-op. Rows tagged with another model are backfilled in the background.
func (s *Stores) SetEmbedder(e Embedder) error {
	if e == nil {
		s.setEmbedder(nil)
		return nil
	}
	s.embedMu.Lock()
	same := s.embedder != nil && s.embedder.Model() == e.Model()
	s.embedMu.Unlock()
	if same {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), activationTimeout)
	defer cancel()
	if _, err := e.Embed(ctx, "ok"); err != nil {
		s.setEmbedder(nil)
		return err
	}
	s.setEmbedder(e)
	return nil
}

func (s *Stores) setEmbedder(e Embedder) {
	s.embedMu.Lock()
	s.embedder = e
	s.embedMu.Unlock()
	for _, d := range s.DBs() {
		d.attach(e)
	}
}

// activeEmbedder returns the provider in use, or nil for the hash embedding.
func (s *Stores) activeEmbedder() Embedder {
	s.embedMu.RLock()
	defer s.embedMu.RUnlock()
	return s.embedder
}

// queryVector embeds query with the active provider. It returns nil and ""
// when there is no provider or it fails, and the caller then uses the hash.
func (s *Stores) queryVector(query string) ([]float32, string) {
	return queryVectorWith(s.activeEmbedder(), query)
}

func queryVectorWith(e Embedder, query string) ([]float32, string) {
	if e == nil {
		return nil, ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), queryEmbedTimeout)
	defer cancel()
	v, err := e.Embed(ctx, query)
	if err != nil || len(v) == 0 {
		return nil, ""
	}
	return v, e.Model()
}

// attach makes d use e for new writes and starts a bounded backfill. A nil e
// turns the provider off for d.
func (d *DB) attach(e Embedder) {
	d.embedMu.Lock()
	d.embedder = e
	d.embedMu.Unlock()
	if e == nil || d.ctx.Err() != nil {
		return
	}
	d.bg.Add(1)
	go func() {
		defer d.bg.Done()
		// Errors are dropped: a backfill that fails is retried by the next
		// activation, and nothing else waits on it.
		_, _ = d.Backfill(d.ctx, e, backfillLimit)
	}()
}

func (d *DB) activeEmbedder() Embedder {
	d.embedMu.RLock()
	defer d.embedMu.RUnlock()
	return d.embedder
}

// queueEmbed embeds a freshly written row in the background. The hash vector
// is already stored, so hints appear at once and the provider vector follows.
func (d *DB) queueEmbed(id string) {
	e := d.activeEmbedder()
	if e == nil || d.ctx.Err() != nil {
		return
	}
	d.bg.Add(1)
	go func() {
		defer d.bg.Done()
		ctx, cancel := context.WithTimeout(d.ctx, embedTimeout)
		defer cancel()
		items, err := d.pendingEmbeds("WHERE id = ?", id)
		if err != nil || len(items) == 0 {
			return
		}
		_ = d.embedPending(ctx, e, items)
	}()
}

// Backfill re-embeds up to limit live rows whose vector was made by another
// model (or by the hash), most recently used first. It returns how many rows
// it updated. A failing provider stops the run and keeps what was done.
func (d *DB) Backfill(ctx context.Context, e Embedder, limit int) (int, error) {
	if e == nil {
		return 0, errors.New("backfill needs an embedder")
	}
	if limit <= 0 {
		limit = backfillLimit
	}
	items, err := d.pendingEmbeds("WHERE embedding_model <> ? AND "+live+" ORDER BY last_used DESC LIMIT ?", e.Model(), limit)
	if err != nil {
		return 0, err
	}
	done := 0
	for start := 0; start < len(items); start += backfillBatch {
		end := min(start+backfillBatch, len(items))
		if err := d.embedPending(ctx, e, items[start:end]); err != nil {
			return done, err
		}
		done += end - start
	}
	return done, nil
}

// embedPending asks e for vectors of items and stores each one that is still
// current.
func (d *DB) embedPending(ctx context.Context, e Embedder, items []pendingEmbed) error {
	texts := make([]string, len(items))
	for i, it := range items {
		texts[i] = it.text
	}
	vecs, err := e.EmbedBatch(ctx, texts)
	if err != nil {
		return err
	}
	return d.storeEmbeds(e.Model(), items, vecs)
}

// pendingEmbeds reads the text of rows matching where. The text is built the
// same way writeRecord builds the hash input, so the two vectors describe the
// same thing.
func (d *DB) pendingEmbeds(where string, args ...any) ([]pendingEmbed, error) {
	rows, err := d.db.Query("SELECT id, title, body, tags, updated FROM memories "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pendingEmbed
	for rows.Next() {
		var id, title, body, tagsJSON, updated string
		if err := rows.Scan(&id, &title, &body, &tagsJSON, &updated); err != nil {
			return nil, err
		}
		var tags []string
		_ = json.Unmarshal([]byte(tagsJSON), &tags)
		out = append(out, pendingEmbed{id: id, text: embedText(title, body, tags), updated: updated})
	}
	return out, rows.Err()
}

// storeEmbeds writes vecs for items in one transaction. The update matches on
// updated, so a row edited while the provider was working keeps its newer
// state and gets embedded again on the next run.
func (d *DB) storeEmbeds(model string, items []pendingEmbed, vecs [][]float32) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, it := range items {
		if _, err := tx.Exec(
			"UPDATE memories SET embedding = ?, embedding_model = ? WHERE id = ? AND updated = ?",
			encodeVec(vecs[i]), model, it.id, it.updated); err != nil {
			return err
		}
	}
	return tx.Commit()
}
