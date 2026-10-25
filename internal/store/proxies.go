package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"jagex_account_creator/internal/proxy"
)

// StoredProxy is one proxy of the store with the health of its attempt rows.
type StoredProxy struct {
	ID      int64
	Proxy   proxy.Proxy
	Enabled bool
	AddedAt time.Time
	Health  ProxyHealth
}

// ProxyHealth holds the totals of the attempt rows whose proxy is the HostPort of one proxy.
type ProxyHealth struct {
	Attempts             int
	Successes            int // the attempts with no failure category
	BrowserBytesSent     int64
	BrowserBytesReceived int64
	HTTPBytesSent        int64
	HTTPBytesReceived    int64
	LastAttemptAt        time.Time // the newest start. Zero with no attempt
	LastFailure          string    // the category of the newest failed attempt. Empty with none
}

// ProxyImport holds the counts of one import of the proxy file text.
type ProxyImport struct {
	Added    int
	Updated  int // the proxies whose credentials changed
	Repeated int // the lines that changed nothing: a proxy that the store holds as it is, or an earlier line of one HostPort
}

const insertProxySQL = `INSERT INTO proxies (host_port, username, password, added_at) VALUES (?, ?, ?, ?)`

const updateProxySQL = `UPDATE proxies SET username = ?, password = ? WHERE host_port = ?`

const setProxyEnabledSQL = `UPDATE proxies SET enabled = ? WHERE id = ?`

const deleteProxySQL = `DELETE FROM proxies WHERE id = ?`

// selectProxyCredentialsSQL lists the columns in the order of proxyCredentials.dest.
const selectProxyCredentialsSQL = `SELECT host_port, username, password FROM proxies`

const selectEnabledProxiesSQL = selectProxyCredentialsSQL + ` WHERE enabled = 1 ORDER BY id`

// selectProxyHealthSQL lists the columns in the order of proxyRow.dest. The join reads the index attempts_proxy.
// Limitation: an attempt that failed outside the proxy, for example on the mailbox, counts against its proxy.
// It fails toward a lower success count.
const selectProxyHealthSQL = `SELECT p.host_port, p.username, p.password, p.id, p.enabled, p.added_at,
	count(t.id), count(t.id) - count(t.category),
	coalesce(sum(t.browser_bytes_sent), 0), coalesce(sum(t.browser_bytes_received), 0),
	coalesce(sum(t.http_bytes_sent), 0), coalesce(sum(t.http_bytes_received), 0),
	coalesce(max(t.started_at), ''),
	coalesce((SELECT f.category FROM attempts f WHERE f.proxy = p.host_port AND f.category IS NOT NULL ORDER BY f.id DESC LIMIT 1), '')
FROM proxies p LEFT JOIN attempts t ON t.proxy = p.host_port`

const selectProxiesSQL = selectProxyHealthSQL + ` GROUP BY p.id ORDER BY p.id`

const selectProxySQL = selectProxyHealthSQL + ` WHERE p.id = ? GROUP BY p.id`

// Proxies returns every proxy with its health, in id order.
func (s *Store) Proxies(ctx context.Context) ([]StoredProxy, error) {
	rows, err := queryRows(ctx, s.db, "proxies", (*proxyRow).dest, selectProxiesSQL)
	if err != nil {
		return nil, err
	}
	return convertRows(rows, (*proxyRow).stored)
}

// Proxy returns the proxy whose id is id, with its health. The error matches
// ErrNotFound when no proxy has that id.
func (s *Store) Proxy(ctx context.Context, id int64) (StoredProxy, error) {
	rows, err := queryRows(ctx, s.db, "proxy", (*proxyRow).dest, selectProxySQL, id)
	if err != nil {
		return StoredProxy{}, err
	}
	proxies, err := convertRows(rows, (*proxyRow).stored)
	if err != nil {
		return StoredProxy{}, err
	}
	if len(proxies) == 0 {
		return StoredProxy{}, fmt.Errorf("reading proxy %d: %w", id, ErrNotFound)
	}
	return proxies[0], nil
}

// EnabledProxies returns the enabled proxies with their credentials, in id order.
func (s *Store) EnabledProxies(ctx context.Context) ([]proxy.Proxy, error) {
	rows, err := queryRows(ctx, s.db, "enabled proxies", (*proxyCredentials).dest, selectEnabledProxiesSQL)
	if err != nil {
		return nil, err
	}
	return convertRows(rows, (*proxyCredentials).parsed)
}

// ImportProxies saves each proxy of the proxy file text in one transaction. A
// new proxy is enabled. A proxy whose HostPort the store holds with other
// credentials gets the new credentials and keeps its enabled state. A bad line
// fails the import with a *proxy.LineError and changes no row.
func (s *Store) ImportProxies(ctx context.Context, text string) (ProxyImport, error) {
	var res ProxyImport
	err := withTx(ctx, s.db, "proxy import", func(tx *sql.Tx) error {
		var err error
		res, err = importProxies(ctx, tx, text, time.Now())
		return err
	})
	if err != nil {
		return ProxyImport{}, err
	}
	return res, nil
}

// SetProxyEnabled sets the enabled state of the proxy whose id is id. The error
// matches ErrNotFound when no proxy has that id.
func (s *Store) SetProxyEnabled(ctx context.Context, id int64, enabled bool) error {
	if err := execByID(ctx, s.db, setProxyEnabledSQL, enabled, id); err != nil {
		return fmt.Errorf("setting enabled of proxy %d: %w", id, err)
	}
	return nil
}

// DeleteProxy deletes the proxy whose id is id. Its attempt rows stay. The
// error matches ErrNotFound when no proxy has that id.
func (s *Store) DeleteProxy(ctx context.Context, id int64) error {
	if err := execByID(ctx, s.db, deleteProxySQL, id); err != nil {
		return fmt.Errorf("deleting proxy %d: %w", id, err)
	}
	return nil
}

// importProxyFile imports the proxy file at path on tx, as ImportProxies does.
// A missing file imports nothing.
func importProxyFile(ctx context.Context, q dbtx, path string) (ProxyImport, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return ProxyImport{}, nil
	}
	if err != nil {
		return ProxyImport{}, fmt.Errorf("importing proxies: %w", err)
	}
	res, err := importProxies(ctx, q, string(data), time.Now())
	if err != nil {
		return ProxyImport{}, fmt.Errorf("importing %s: %w", path, err)
	}
	return res, nil
}

// importProxies saves each proxy of text on q, with now as the added_at of a new row.
func importProxies(ctx context.Context, q dbtx, text string, now time.Time) (ProxyImport, error) {
	proxies, repeated, err := proxy.Parse(text)
	if err != nil {
		return ProxyImport{}, err
	}
	rows, err := queryRows(ctx, q, "proxies", (*proxyCredentials).dest, selectProxyCredentialsSQL)
	if err != nil {
		return ProxyImport{}, err
	}
	stored := make(map[string]proxyCredentials, len(rows))
	for _, r := range rows {
		stored[r.hostPort] = r
	}
	res := ProxyImport{Repeated: repeated}
	for _, p := range proxies {
		old, ok := stored[p.HostPort()]
		switch {
		case !ok:
			_, err = q.ExecContext(ctx, insertProxySQL, p.HostPort(), p.Username, p.Password, now.UTC().Format(timeLayout))
			res.Added++
		case old.username != p.Username || old.password != p.Password:
			_, err = q.ExecContext(ctx, updateProxySQL, p.Username, p.Password, p.HostPort())
			res.Updated++
		default:
			res.Repeated++
		}
		if err != nil {
			return ProxyImport{}, fmt.Errorf("saving proxy %s: %w", p.HostPort(), err)
		}
	}
	return res, nil
}

// execByID runs query with args, whose last value is a row id. The error is
// ErrNotFound when the statement matched no row.
func execByID(ctx context.Context, q dbtx, query string, args ...any) error {
	res, err := q.ExecContext(ctx, query, args...)
	var n int64
	if err == nil {
		// SQLite counts each row that the WHERE matched, also a row that the statement left as it was.
		n, err = res.RowsAffected()
	}
	if err == nil && n == 0 {
		err = ErrNotFound
	}
	return err
}

// proxyCredentials is one row of selectProxyCredentialsSQL.
type proxyCredentials struct {
	hostPort, username, password string
}

func (x *proxyCredentials) dest() []any {
	return []any{&x.hostPort, &x.username, &x.password}
}

// parsed returns the proxy of x. The one parser reads the scheme, the host and the port from the HostPort.
func (x *proxyCredentials) parsed() (proxy.Proxy, error) {
	p, err := proxy.ParseLine(x.hostPort)
	if err != nil {
		return p, fmt.Errorf("reading proxy %s: %w", x.hostPort, err)
	}
	p.Username, p.Password = x.username, x.password
	return p, nil
}

// proxyRow is one row of selectProxyHealthSQL, with the times as stored.
type proxyRow struct {
	creds                proxyCredentials
	p                    StoredProxy
	addedAt, lastAttempt string
}

func (x *proxyRow) dest() []any {
	h := &x.p.Health
	return append(x.creds.dest(), &x.p.ID, &x.p.Enabled, &x.addedAt,
		&h.Attempts, &h.Successes, &h.BrowserBytesSent, &h.BrowserBytesReceived, &h.HTTPBytesSent, &h.HTTPBytesReceived,
		&x.lastAttempt, &h.LastFailure)
}

// stored returns the proxy of x with its proxy and its times parsed.
func (x *proxyRow) stored() (StoredProxy, error) {
	sp := x.p
	var err error
	if sp.Proxy, err = x.creds.parsed(); err != nil {
		return sp, err
	}
	sp.AddedAt, err = time.Parse(timeLayout, x.addedAt)
	if err == nil && x.lastAttempt != "" {
		sp.Health.LastAttemptAt, err = time.Parse(timeLayout, x.lastAttempt)
	}
	if err != nil {
		return sp, fmt.Errorf("reading the times of proxy %d: %w", sp.ID, err)
	}
	return sp, nil
}
