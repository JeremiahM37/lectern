package headless

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/app"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm/registry"
)

// listModelsTimeout bounds one provider's model list request.
const listModelsTimeout = 30 * time.Second

// manage runs the provider management flags: --list-providers,
// --list-models and --set-key. It does not open a session.
func manage(ctx context.Context, o options, cwd string, stdout, stderr io.Writer) int {
	cfg, err := config.Load(app.FindRoot(cwd))
	if err != nil {
		fmt.Fprintf(stderr, "scribe: config: %v\n", err)
		return 1
	}
	reg := registry.New(cfg)
	switch {
	case o.setKey:
		if len(o.keyArgs) != 2 {
			fmt.Fprintln(stderr, "scribe: usage: --set-key PROVIDER KEY\n\nProviders:")
			writeProviders(stderr, reg)
			return 1
		}
		provider, key := o.keyArgs[0], o.keyArgs[1]
		if err := reg.SetKey(provider, key); err != nil {
			fmt.Fprintf(stderr, "scribe: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "saved the key for %s\n", provider)
		return 0
	case o.listProviders:
		writeProviders(stdout, reg)
		return 0
	default:
		return listModels(ctx, o, cfg, reg, stdout, stderr)
	}
}

// writeProviders prints the provider table: id, name, key status, default model.
func writeProviders(w io.Writer, reg *registry.Registry) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PROVIDER\tNAME\tKEY\tDEFAULT MODEL")
	for _, p := range reg.List() {
		def := p.DefaultModel
		if def == "" {
			def = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", p.ID, p.Name, keyCell(p), def)
	}
	tw.Flush()
}

func keyCell(p registry.ProviderInfo) string {
	switch {
	case p.HasKey:
		return "✓ " + p.KeySource
	case p.NeedsKey:
		return "✗ no key"
	default:
		return "- not needed"
	}
}

// listModels prints the models of one provider as provider/model refs, so a
// line can be pasted straight into --model. Without a provider it uses the
// provider of the configured model.
func listModels(ctx context.Context, o options, cfg config.Config, reg *registry.Registry, stdout, stderr io.Writer) int {
	provider := o.listModelsOf
	if provider == "" {
		provider, _ = llm.SplitModelRef(firstNonEmpty(o.model, cfg.Model))
	}
	if provider == "" {
		fmt.Fprintln(stderr, "scribe: name a provider: --list-models PROVIDER (see --list-providers)")
		return 1
	}
	ctx, cancel := context.WithTimeout(ctx, listModelsTimeout)
	defer cancel()
	models, err := reg.ListModels(ctx, provider)
	if err != nil && len(models) == 0 {
		fmt.Fprintf(stderr, "scribe: %s: %v\n", provider, err)
		return 1
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "MODEL\tNAME\tCONTEXT\t$/MTOK IN\t$/MTOK OUT")
	for _, m := range models {
		ctxWin := "-"
		if m.ContextWindow > 0 {
			ctxWin = humanTokens(m.ContextWindow)
		}
		fmt.Fprintf(tw, "%s/%s\t%s\t%s\t%s\t%s\n", provider, m.ID, m.Name, ctxWin, price(m.InputPerMTok), price(m.OutputPerMTok))
	}
	tw.Flush()
	if err != nil {
		fmt.Fprintf(stderr, "scribe: warning: %s: %v (the list may be incomplete)\n", provider, err)
	}
	return 0
}

func price(v float64) string {
	if v == 0 {
		return "-"
	}
	return fmt.Sprintf("%.2f", v)
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}
