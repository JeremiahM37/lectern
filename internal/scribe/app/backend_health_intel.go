package app

// REAUDIT5 01#4 and 03#24 (spec 04 section 6.1, spec 02 DiagnosePopup). The
// Health Check asks each language server one real operation, documentSymbol on
// a file of the project's language. Starting the server gets 10 s and the
// operation 5 s (lsp.ProbeInitTimeout, lsp.ProbeOpTimeout). Each language
// reports as soon as its probe ends, so the popup fills in. The whole run is
// bounded by the popup's 90 s (tui.healthPopupTimeout).

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/lsp"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui"
)

// maxHealthLanguages caps the language servers one run starts. A project with
// many languages gets the first ones lsp.DetectLanguages reports.
const maxHealthLanguages = 6

// HealthChecksStream implements tui.HealthStreamer. The quick checks go first
// as one batch, then each language server probe reports by itself. report
// gets the sorted result so far each time a block is ready.
func (b *scribeBackend) HealthChecksStream(report func([]tui.HealthBackend)) []tui.HealthBackend {
	a := b.current()
	var mu sync.Mutex
	var acc []tui.HealthBackend
	publish := func(bs []tui.HealthBackend) {
		mu.Lock()
		defer mu.Unlock()
		acc = append(acc, bs...)
		report(sortHealth(slices.Clone(acc), healthBackendOrder))
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer guardHealth(func(r any) { publish(panickedBackends([]string{"Model providers"}, r)) })
		publish(b.quickHealth())
	}()
	for _, lang := range healthLanguages(a) {
		wg.Add(1)
		go func(lang string) {
			defer wg.Done()
			defer guardHealth(func(r any) {
				publish(panickedBackends([]string{lspBlockName(lang)}, r))
			})
			publish([]tui.HealthBackend{lspProbeBlock(a, lang)})
		}(lang)
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	return sortHealth(slices.Clone(acc), healthBackendOrder)
}

// guardHealth turns a panic in a check into a failing row, so one bad probe
// cannot take the TUI down. It is deferred directly by the caller.
func guardHealth(onPanic func(r any)) {
	if r := recover(); r != nil {
		onPanic(r)
	}
}

// healthLanguages lists the languages to probe: the project's languages that
// have a language server catalogued, capped at maxHealthLanguages.
func healthLanguages(a *App) []string {
	if a.ext.lsp == nil {
		return nil
	}
	langs := lsp.DetectLanguages(a.Workdir)
	slices.Sort(langs) // detection walks a map, so sort for a stable cap
	if len(langs) > maxHealthLanguages {
		langs = langs[:maxHealthLanguages]
	}
	return langs
}

// lspBlockName is the popup block for one language server backend.
func lspBlockName(lang string) string {
	return "LSP " + lang + " (tier 1)"
}

// lspProbeBlock probes one language's server and returns its block.
func lspProbeBlock(a *App, lang string) tui.HealthBackend {
	hb := tui.HealthBackend{Name: lspBlockName(lang)}
	if slices.Contains(a.Cfg.DisabledLSPServers, lang) {
		hb.Probes = []tui.HealthProbe{{Name: "documentSymbol", State: "info",
			Reason: "turned off in config", Ms: -1}}
		return hb
	}
	file, ok := lsp.SampleFile(a.Workdir, lang)
	if !ok {
		hb.Probes = []tui.HealthProbe{{Name: "documentSymbol", State: "info",
			Reason: "no " + lang + " file in the project to ask about", Ms: -1}}
		return hb
	}
	res := a.ext.lsp.Probe(context.Background(), file)
	p := tui.HealthProbe{Name: "documentSymbol", Ms: -1}
	switch {
	case res.Unsupported:
		p.State, p.Reason = "info", "no language server is catalogued for this file"
	case res.InitErr != nil:
		p.State, p.Reason = "fail", oneLineStr("did not start: "+res.InitErr.Error())
		p.Fix = "install the language server, then run /diagnose again"
	case res.OpErr != nil:
		p.State, p.Reason = "fail", oneLineStr(res.OpErr.Error())
		p.Fix = "the server started but did not answer; see /lsp for its state"
	default:
		p.State = "pass"
		p.Ms = int(res.Elapsed.Milliseconds())
		p.Reason = fmt.Sprintf("%d symbols in %s", res.Symbols, res.File)
	}
	hb.Probes = []tui.HealthProbe{p}
	return hb
}
