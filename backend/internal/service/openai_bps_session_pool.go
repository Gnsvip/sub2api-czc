package service

import "context"

type bpsProxySource interface {
	ListActive(context.Context) ([]Proxy, error)
}
