package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"jagex_account_creator/internal/api"
	"jagex_account_creator/internal/proxy"
	"jagex_account_creator/internal/store"
)

// ListProxies returns every proxy with its health, in id order. No item holds
// a credential.
func (s *apiServer) ListProxies(ctx context.Context, _ api.ListProxiesRequestObject) (api.ListProxiesResponseObject, error) {
	proxies, err := s.store.Proxies(ctx)
	if err != nil {
		return nil, err
	}
	out := api.ProxyList{Proxies: make([]api.Proxy, len(proxies))}
	for i := range proxies {
		out.Proxies[i] = apiProxy(&proxies[i])
	}
	return api.ListProxies200JSONResponse(out), nil
}

// ImportProxies saves each proxy of the posted proxy file text. A bad line
// changes no proxy.
func (s *apiServer) ImportProxies(ctx context.Context, r api.ImportProxiesRequestObject) (api.ImportProxiesResponseObject, error) {
	res, err := s.store.ImportProxies(ctx, *r.Body)
	if lineErr, ok := errors.AsType[*proxy.LineError](err); ok {
		// The error names the line by its number only, so the answer holds no credential.
		return api.ImportProxies400JSONResponse{Message: lineErr.Error()}, nil
	}
	if err != nil {
		return nil, err
	}
	s.logger.Info("proxies imported", "added", res.Added, "updated", res.Updated, "repeated", res.Repeated)
	return api.ImportProxies200JSONResponse{Added: res.Added, Updated: res.Updated, Repeated: res.Repeated}, nil
}

// ExportProxies returns the proxy file of every proxy, credentials included,
// so an import of the file gives the same proxies.
func (s *apiServer) ExportProxies(ctx context.Context, _ api.ExportProxiesRequestObject) (api.ExportProxiesResponseObject, error) {
	proxies, err := s.store.Proxies(ctx)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	for i := range proxies {
		b.WriteString(proxies[i].Proxy.Line() + "\n")
	}
	headers := api.ExportProxies200ResponseHeaders{ContentDisposition: `attachment; filename="proxies.txt"`}
	return api.ExportProxies200TextResponse{Body: b.String(), Headers: headers}, nil
}

// EnableProxy enables one proxy and returns it. A second call changes nothing.
func (s *apiServer) EnableProxy(ctx context.Context, r api.EnableProxyRequestObject) (api.EnableProxyResponseObject, error) {
	p, err := s.setProxyEnabled(ctx, r.Id, true, "proxy enabled")
	if errors.Is(err, store.ErrNotFound) {
		return api.EnableProxy404JSONResponse{NotFoundJSONResponse: notFound(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	return api.EnableProxy200JSONResponse(p), nil
}

// DisableProxy disables one proxy and returns it. A second call changes nothing.
func (s *apiServer) DisableProxy(ctx context.Context, r api.DisableProxyRequestObject) (api.DisableProxyResponseObject, error) {
	p, err := s.setProxyEnabled(ctx, r.Id, false, "proxy disabled")
	if errors.Is(err, store.ErrNotFound) {
		return api.DisableProxy404JSONResponse{NotFoundJSONResponse: notFound(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	return api.DisableProxy200JSONResponse(p), nil
}

// setProxyEnabled sets the enabled state of the proxy id, logs msg and returns
// the proxy.
func (s *apiServer) setProxyEnabled(ctx context.Context, id int64, enabled bool, msg string) (api.Proxy, error) {
	err := s.store.SetProxyEnabled(ctx, id, enabled)
	var p store.StoredProxy
	if err == nil {
		s.logger.Info(msg, "id", id)
		// A delete between the write and the read gets 404.
		p, err = s.store.Proxy(ctx, id)
	}
	if err != nil {
		return api.Proxy{}, err
	}
	return apiProxy(&p), nil
}

// DeleteProxy deletes one proxy. Its attempt rows stay.
func (s *apiServer) DeleteProxy(ctx context.Context, r api.DeleteProxyRequestObject) (api.DeleteProxyResponseObject, error) {
	err := s.store.DeleteProxy(ctx, r.Id)
	if errors.Is(err, store.ErrNotFound) {
		return api.DeleteProxy404JSONResponse{NotFoundJSONResponse: notFound(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	s.logger.Info("proxy deleted", "id", r.Id)
	return api.DeleteProxy204Response{}, nil
}

// TestProxy runs the connectivity check of an attempt on one proxy, through a
// forward proxy of its own. It changes no cooldown, because no run reads it.
// Limitation: the check opens a TCP connection only, so it does not check the credentials.
func (s *apiServer) TestProxy(ctx context.Context, r api.TestProxyRequestObject) (api.TestProxyResponseObject, error) {
	p, err := s.store.Proxy(ctx, r.Id)
	var fwd *proxy.ForwardProxy
	if err == nil {
		// A forward proxy that does not start is a fault of the server, not a result of the proxy, so it gets 500.
		fwd, _, err = startForwardProxy(ctx, s.logger, p.Proxy)
	}
	if errors.Is(err, store.ErrNotFound) {
		return api.TestProxy404JSONResponse{NotFoundJSONResponse: notFound(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	start := time.Now()
	err = fwd.CheckUpstream(ctx)
	fwd.Stop()
	result := api.ProxyTest{Ok: err == nil, DurationMs: time.Since(start).Milliseconds()}
	if err != nil {
		// The dial error names the address of the upstream and no credential.
		result.Error = new(err.Error())
	}
	return api.TestProxy200JSONResponse(result), nil
}

// apiProxy returns the API form of p, with no credential.
func apiProxy(p *store.StoredProxy) api.Proxy {
	h := &p.Health
	out := api.Proxy{
		Id:                   p.ID,
		Proxy:                p.Proxy.HostPort(),
		Auth:                 p.Proxy.Username != "",
		Enabled:              p.Enabled,
		AddedAt:              p.AddedAt,
		Attempts:             h.Attempts,
		Successes:            h.Successes,
		BrowserBytesSent:     h.BrowserBytesSent,
		BrowserBytesReceived: h.BrowserBytesReceived,
		HttpBytesSent:        h.HTTPBytesSent,
		HttpBytesReceived:    h.HTTPBytesReceived,
		LastFailure:          optional(h.LastFailure),
	}
	if !h.LastAttemptAt.IsZero() {
		out.LastAttemptAt = new(h.LastAttemptAt)
	}
	return out
}
