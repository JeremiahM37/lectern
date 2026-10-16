//go:build !unix

package nvim

// sweepStale does nothing where the socket owner cannot be probed.
func sweepStale() {}
