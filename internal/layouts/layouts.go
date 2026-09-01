package layouts

import "context"

type Layout struct {
	ID   string
	Name string
}

type Subscription interface {
	Events() <-chan Layout
	Errors() <-chan error
	Close() error
}

type Source interface {
	List(ctx context.Context) ([]Layout, error)
	Current(ctx context.Context) (Layout, error)
	Subscribe(ctx context.Context) (Subscription, error)
}
