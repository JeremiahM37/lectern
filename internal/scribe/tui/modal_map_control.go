package tui

// MapPrefs are the code map settings the /repo-map modal edits.
type MapPrefs struct {
	Mode         string // off, ast, synthetic, llm or full
	SummaryLimit int    // files summarised per pass (llm and full only)
	AutoRegen    bool   // regenerate summaries after a rebuild
	Budget       int    // tokens for the rendered map; 0 means auto
}

// MapController is implemented by a backend that can change the code map. The
// /repo-map modal uses it for its actions. A backend without it still shows
// the status, and the modal says the actions are unavailable.
type MapController interface {
	MapPrefs() MapPrefs
	SetMapPrefs(MapPrefs) error
	ClearMap() error
	ClearSummaries() error
	RegenerateSummaries() error
}

// mapModes are the summary modes in the order the modal lists them. The keys
// 1 to 5 pick them.
var mapModes = []string{"off", "ast", "synthetic", "llm", "full"}

// mapLimits and mapBudgets are the values ←/→ step through. Budget 0 is auto.
var (
	mapLimits  = []int{100, 200, 300, 500}
	mapBudgets = []int{0, 2000, 4000, 8000, 16000}
)

// defaultMapPrefs is the modal's starting point when the backend has no
// controller or has no saved choice.
func defaultMapPrefs() MapPrefs {
	return MapPrefs{Mode: "ast", SummaryLimit: 300}
}

func mapIndexOf[T comparable](xs []T, v T) int {
	for i, x := range xs {
		if x == v {
			return i
		}
	}
	return 0
}
