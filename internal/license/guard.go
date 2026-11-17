package license

import (
	"errors"
	"fmt"
	"slices"
)

// Errors returned by the guard. HTTP handlers map them to 402/403 with the message.
var (
	ErrLocked      = errors.New("لایسنس فعال نیست؛ فروش و ساخت سرویس جدید متوقف است")
	ErrUnactivated = errors.New("لایسنس فعال نشده است")
)

type ModuleError struct{ Module string }

func (e ModuleError) Error() string {
	return fmt.Sprintf("این قابلیت (%s) در لایسنس شما فعال نیست", e.Module)
}

type LimitError struct {
	Limit string
	Max   int64
}

func (e LimitError) Error() string {
	return fmt.Sprintf("به سقف لایسنس رسیده‌اید (%s: %d)", e.Limit, e.Max)
}

// RequireActive allows actions that create or sell things.
func (m *Manager) RequireActive() error {
	switch m.Status().State {
	case StateActive:
		return nil
	case StateUnactivated:
		return ErrUnactivated
	default:
		return ErrLocked
	}
}

func (m *Manager) HasModule(mod string) bool {
	s := m.Status()
	return s.State == StateActive && slices.Contains(s.Modules, mod)
}

// RequireModule = RequireActive + the module is licensed.
func (m *Manager) RequireModule(mod string) error {
	if err := m.RequireActive(); err != nil {
		return err
	}
	if !m.HasModule(mod) {
		return ModuleError{mod}
	}
	return nil
}

// Limit returns the licensed maximum (-1 = unlimited, 0 = none).
func (m *Manager) Limit(name string) int64 {
	s := m.Status()
	if s.State != StateActive {
		return 0
	}
	return s.Limits[name]
}

// RequireBelow checks that adding one more item keeps current under the limit.
func (m *Manager) RequireBelow(name string, current int64) error {
	if err := m.RequireActive(); err != nil {
		return err
	}
	max := m.Limit(name)
	if max < 0 {
		return nil
	}
	if current >= max {
		return LimitError{name, max}
	}
	return nil
}
