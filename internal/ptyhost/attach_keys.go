package ptyhost

// attachKeys parses the reserved attach chords across arbitrary input reads.
// A terminal may deliver both bytes of a chord in one read, or split them.
type attachKeys struct{ prefix bool }

func (k *attachKeys) feed(input []byte) (output []byte, detach bool) {
	for _, b := range input {
		if k.prefix {
			k.prefix = false
			switch b {
			case 'd':
				return output, true
			case detachPrefix:
				output = append(output, b)
				continue
			default:
				output = append(output, detachPrefix)
			}
		}
		switch b {
		case detachPrefix:
			k.prefix = true
		case 0x1c:
			// Ctrl-\ belongs to Lectern's upload controls. A bare PTY
			// attachment must never pass it as SIGQUIT to the agent.
		default:
			output = append(output, b)
		}
	}
	return output, false
}
