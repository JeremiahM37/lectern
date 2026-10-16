package repomap

import (
	"cmp"
	"encoding/binary"
	"math/bits"
	"slices"
)

// Clone signatures are computed while a file is parsed and stored with it, so
// a warm rebuild does not repeat the work. Everything here works on the masked
// source (strings and comments removed) and needs no parser.
//
// Three signals are kept:
//   - a near-duplicate signature per file: one-permutation MinHash over 3-token
//     shingles with cloneBins bins (the spec's 128-hash MinHash, computed in one
//     hash per shingle instead of one per hash);
//   - sampled fragment hashes over cloneWindow-token windows, kept when the
//     window hash falls in 1/fragSample of the space, so the same fragment is
//     sampled in every file that contains it;
//   - a shape hash per function body, with identifiers and literals collapsed,
//     for structural duplicates (exact duplicates are BodyHash, see Duplicates).

const (
	cloneBins      = 128
	cloneShingle   = 3
	cloneWindow    = 16
	fragBits       = 4 // 1 in 2^fragBits windows is sampled
	fragMul        = 0x9e3779b97f4a7c15
	shapeMinTokens = 30
	// minCloneShingles is how many bins a file must fill before its signature
	// means anything (a bin fills with the first shingle that lands in it).
	// Smaller files would match on chance.
	minCloneShingles = 24
	// nearDupMin is the estimated Jaccard similarity above which two files are
	// reported as near-duplicates.
	nearDupMin = 0.7
	bandRows   = 4
	// maxFragFiles caps the files a fragment may appear in before it is treated
	// as boilerplate (licence headers, generated code) and ignored.
	maxFragFiles = 20
	// minFragShared is how many sampled fragments two files must share to be
	// reported as having a repeated fragment.
	minFragShared = 2
)

// cloneSig is the one-permutation MinHash of a file's shingles.
type cloneSig [cloneBins]uint32

const (
	shapeIdent = 0x1d1e7
	shapeNum   = 0x2b0b3
)

// scanTokens calls fn for every token of masked source, in order: an
// identifier or number run, or a single punctuation byte. h is the hash of the
// token's text and sh its shape hash (identifiers and numbers collapse to one
// placeholder each). Nothing is stored, so a large file costs no memory beyond
// its own source.
func scanTokens(m []byte, fn func(h, sh uint64)) {
	for i := 0; i < len(m); {
		c := m[i]
		if isSpaceByte(c) {
			i++
			continue
		}
		start := i
		if isIdentPart(c) {
			for i < len(m) && isIdentPart(m[i]) {
				i++
			}
			sh := uint64(shapeIdent)
			if c >= '0' && c <= '9' {
				sh = shapeNum
			}
			fn(hashBytes64(m[start:i]), sh)
			continue
		}
		i++
		h := hashBytes64(m[start:i])
		fn(h, h)
	}
}

func hashBytes64(b []byte) uint64 {
	h := uint64(14695981039346656037)
	for _, c := range b {
		h = (h ^ uint64(c)) * 1099511628211
	}
	return h
}

// mix64 is a splitmix-style finaliser, so that nearby inputs spread across
// the bins and the sampled fragments.
func mix64(x uint64) uint64 {
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31
	return x
}

// fileSignatures computes the near-duplicate signature and the sampled
// fragment hashes of a file's masked source in one streaming pass. The
// signature is nil when the file has too few filled bins to compare.
func fileSignatures(m []byte) (*cloneSig, []uint32) {
	var sig cloneSig
	for i := range sig {
		sig[i] = ^uint32(0)
	}
	// Ring of the last cloneWindow token hashes. A shingle is three of them;
	// a fragment is all of them, hashed with a rolling polynomial.
	const base = 0x100000001b3
	var pow uint64 = 1
	for i := 0; i < cloneWindow-1; i++ {
		pow *= base
	}
	var ring [cloneWindow]uint64
	n := 0 // tokens seen
	var roll uint64
	var frags []uint32
	scanTokens(m, func(h, _ uint64) {
		slot := n % cloneWindow
		if n >= cloneWindow {
			roll -= ring[slot] * pow
		}
		ring[slot] = h
		roll = roll*base + h
		n++
		if n >= cloneShingle {
			a := ring[(n-3)%cloneWindow]
			b := ring[(n-2)%cloneWindow]
			c := ring[(n-1)%cloneWindow]
			sh := mix64(a ^ bits.RotateLeft64(b, 21) ^ bits.RotateLeft64(c, 42))
			if v := uint32(sh); v < sig[sh>>57] {
				sig[sh>>57] = v
			}
		}
		if n >= cloneWindow {
			// One multiply decides the sample: the top bits of the product
			// are well mixed, so 1 in fragSample windows are kept.
			if (roll*fragMul)>>(64-fragBits) == 0 {
				// Stored as 32 bits: a file's sampled fragments are persisted
				// with the file, and 32 bits keeps that cache small. A chance
				// collision between two files needs two shared fragments to
				// count, so it is negligible.
				frags = append(frags, uint32(mix64(roll)))
			}
		}
	})
	frags = sortUnique(frags)
	// Filled bins stand in for distinct shingles: a file needs enough filled
	// bins to compare meaningfully. This avoids a map insert per shingle.
	distinct := 0
	for _, v := range sig {
		if v != ^uint32(0) {
			distinct++
		}
	}
	if distinct < minCloneShingles {
		return nil, frags
	}
	densify(&sig)
	return &sig, frags
}

// densify fills empty bins from the nearest filled bin after them, with an
// offset per step, so that a sparse signature still compares bin by bin.
func densify(sig *cloneSig) {
	orig := *sig
	for i := range sig {
		if orig[i] != ^uint32(0) {
			continue
		}
		for k := 1; k < cloneBins; k++ {
			j := (i + k) % cloneBins
			if orig[j] != ^uint32(0) {
				sig[i] = orig[j] + uint32(k)*0x9e3779b9
				break
			}
		}
	}
}

// similarity estimates the Jaccard similarity of two file signatures.
func similarity(a, b *cloneSig) float64 {
	eq := 0
	for i := range a {
		if a[i] == b[i] {
			eq++
		}
	}
	return float64(eq) / cloneBins
}

// shapeHash hashes the shape tokens of masked[start:end], a function body. It
// returns "" for bodies shorter than shapeMinTokens.
func shapeHash(m []byte, start, end int) string {
	if start < 0 || end > len(m) || start >= end {
		return ""
	}
	var h uint64 = 1469598103934665603
	n := 0
	scanTokens(m[start:end], func(_, sh uint64) {
		h = (h ^ sh) * 1099511628211
		n++
	})
	if n < shapeMinTokens {
		return ""
	}
	return hex64(mix64(h))
}

func hex64(v uint64) string {
	const digits = "0123456789abcdef"
	var b [16]byte
	for i := 15; i >= 0; i-- {
		b[i] = digits[v&15]
		v >>= 4
	}
	return string(b[:])
}

// bandKey hashes one band of a signature (bandRows bins).
func bandKey(sig *cloneSig, band int) uint64 {
	var buf [bandRows * 4]byte
	for r := 0; r < bandRows; r++ {
		binary.LittleEndian.PutUint32(buf[r*4:], sig[band*bandRows+r])
	}
	return hashBytes64(buf[:])
}

func sortUnique[T cmp.Ordered](v []T) []T {
	if len(v) < 2 {
		return v
	}
	slices.Sort(v)
	out := v[:1]
	for _, x := range v[1:] {
		if x != out[len(out)-1] {
			out = append(out, x)
		}
	}
	return out
}
