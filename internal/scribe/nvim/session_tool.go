package nvim

import "context"

// switchTo opens abs in the editor. It reports whether the switch happened,
// and the reason when it did not.
func (s *Session) switchTo(ctx context.Context, abs string) (bool, string, error) {
	v, err := s.Lua(ctx, switchLua, abs)
	if err != nil {
		return false, "", err
	}
	return luaResult(v)
}

// currentName is the current buffer's file name, or "" for a buffer with none.
func (s *Session) currentName(ctx context.Context) (string, error) {
	v, err := s.Call(ctx, "nvim_buf_get_name", int64(0))
	if err != nil {
		return "", err
	}
	name, _ := v.(string)
	return name, nil
}

// currentAbs is the current buffer's absolute file name, or "".
func (s *Session) currentAbs(ctx context.Context) string {
	name, err := s.currentName(ctx)
	if err != nil || name == "" {
		return ""
	}
	return name
}
