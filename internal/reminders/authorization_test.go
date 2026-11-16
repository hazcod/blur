package reminders_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/hazcod/blur/internal/reminders"
)

type authorizationCheckPort struct {
	reminders.UnavailablePort
	reads   atomic.Int32
	prompts atomic.Int32
	auth    reminders.Authorization
	err     error
}

func (p *authorizationCheckPort) Authorization(ctx context.Context) (reminders.Authorization, error) {
	p.reads.Add(1)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return p.auth, p.err
}
func (p *authorizationCheckPort) RequestAuthorization(context.Context) (reminders.Authorization, error) {
	p.prompts.Add(1)
	return reminders.Allowed, nil
}

func TestReadOnlyNotificationAuthorizationNeverPrompts(t *testing.T) {
	for _, auth := range []reminders.Authorization{reminders.NotDetermined, reminders.Denied, reminders.Allowed, reminders.Unsupported} {
		t.Run(string(auth), func(t *testing.T) {
			port := &authorizationCheckPort{auth: auth}
			svc, err := reminders.NewService(store(t), port, reminders.Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()
			got, err := svc.Authorization(context.Background())
			if err != nil || got != auth || svc.Status().Authorization != auth || port.reads.Load() != 1 || port.prompts.Load() != 0 {
				t.Fatalf("read-only auth=%q err=%v reads=%d prompts=%d", got, err, port.reads.Load(), port.prompts.Load())
			}
			canceled, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := svc.Authorization(canceled); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled authorization = %v", err)
			}
		})
	}
}

func TestReadOnlyNotificationAuthorizationRejectsFailedAndUnknownResults(t *testing.T) {
	for _, port := range []*authorizationCheckPort{{auth: reminders.Allowed, err: errors.New("synthetic error")}, {auth: reminders.Authorization("unexpected")}} {
		svc, err := reminders.NewService(store(t), port, reminders.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Authorization(context.Background()); err == nil || svc.Status().Authorization == reminders.Allowed || port.prompts.Load() != 0 {
			t.Fatal("invalid/failed port result accepted")
		}
		svc.Close()
		if _, err := svc.Authorization(context.Background()); !errors.Is(err, reminders.ErrUnavailable) {
			t.Fatalf("closed service check = %v", err)
		}
	}
}
