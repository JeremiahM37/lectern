//go:build race

package chat

// raceBudgetScale relaxes timing assertions under the race detector, which
// slows allocation-heavy rendering several-fold.
const raceBudgetScale = 8
