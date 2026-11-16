package reminders

import "context"

// Authorization refreshes permission state through the existing notification
// port. It does not request access, queue notices, or wake reminder delivery.
func (s *Service) Authorization(parent context.Context) (Authorization, error) {
	if parent != nil {
		if err := parent.Err(); err != nil {
			return "", err
		}
	}
	ctx, done, err := s.enter(parent)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		done()
		return "", err
	}
	auth, err := s.port.Authorization(ctx)
	if err == nil {
		switch auth {
		case Allowed, Denied, NotDetermined, Unsupported:
		default:
			err = ErrUnavailable
		}
	}
	if err == nil {
		s.mu.Lock()
		s.status.Authorization = auth
		s.mu.Unlock()
	}
	done()
	if err == nil {
		s.changed()
	}
	return auth, err
}
