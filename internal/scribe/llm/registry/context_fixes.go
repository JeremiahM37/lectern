package registry

// providerWindowOverrides lists context windows that a provider reports
// wrongly, keyed by "provider/model". Spec 03 section 4.10 puts this list
// after the user's own override and before the provider's API data, so a
// known-wrong upstream value never reaches the answer. A user override always
// wins over an entry here.
//
// The table is empty. The behavioural spec describes the mechanism but names
// no wrong values, and a number taken from memory would be a guess. Add an
// entry only with a figure from the provider's documentation or a measured
// request, and say where it came from in the comment beside it.
var providerWindowOverrides = map[string]int{}

// providerWindow returns the corrected window for ref, when the registry has
// one for it.
func providerWindow(ref string) (int, bool) {
	w, ok := providerWindowOverrides[ref]
	return w, ok && w > 0
}

// hasContextOverride reports whether the user set a window for ref.
func (r *Registry) hasContextOverride(ref string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.contextOverrides[ref]
	return ok && w > 0
}
