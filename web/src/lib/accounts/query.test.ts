import { describe, expect, it } from 'vitest';
import {
  exportHref,
  filterQuery,
  hasFilters,
  listQuery,
  pageQuery,
  readFilters,
  type Filters,
} from './query.ts';

const none: Filters = {
  run: '',
  unconfirmed: '',
  totp: '',
  proxy: '',
  created_from: '',
  created_to: '',
  q: '',
};

// The form submit of an empty form sends every field with an empty value.
const emptyForm = 'run=&unconfirmed=&totp=&proxy=&created_from=&created_to=&q=';

// The UTC text of a local time, built with no text parse.
function utc(year: number, month: number, day: number, hour: number, minute: number): string {
  return new Date(year, month - 1, day, hour, minute).toISOString();
}

describe('readFilters', () => {
  it('reads each filter of the page query', () => {
    const page = new URLSearchParams(
      'run=3&unconfirmed=true&totp=false&proxy=h%3A1&created_from=2026-02-15T10%3A30&created_to=x&q=Bob&before=9',
    );
    expect(readFilters(page)).toEqual({
      run: '3',
      unconfirmed: 'true',
      totp: 'false',
      proxy: 'h:1',
      created_from: '2026-02-15T10:30',
      created_to: 'x',
      q: 'Bob',
    });
  });

  it('gives an empty value for a missing filter', () => {
    expect(readFilters(new URLSearchParams())).toEqual(none);
  });
});

describe('listQuery', () => {
  it('drops each empty value', () => {
    expect(listQuery(new URLSearchParams(emptyForm))).toBe('');
    expect(listQuery(new URLSearchParams(`${emptyForm}&before=`))).toBe('');
  });

  it('keeps each set filter and the before', () => {
    const page = new URLSearchParams(
      'run=3&unconfirmed=true&totp=false&proxy=h%3A1&q=Bob&before=9',
    );
    expect(Object.fromEntries(new URLSearchParams(listQuery(page)))).toEqual({
      run: '3',
      unconfirmed: 'true',
      totp: 'false',
      proxy: 'h:1',
      q: 'Bob',
      before: '9',
    });
  });

  it('turns each local time of the form into RFC 3339 in UTC', () => {
    const page = new URLSearchParams('created_from=2026-02-15T10:30&created_to=2026-07-01T00:05');
    const query = new URLSearchParams(listQuery(page));
    expect(query.get('created_from')).toBe(utc(2026, 2, 15, 10, 30));
    expect(query.get('created_to')).toBe(utc(2026, 7, 1, 0, 5));
  });

  it('sends a time that does not parse as it is, so the server refuses it', () => {
    const query = new URLSearchParams(listQuery(new URLSearchParams('created_from=soon')));
    expect(query.get('created_from')).toBe('soon');
  });

  it('leaves the other filters as the form sent them', () => {
    const query = new URLSearchParams(listQuery(new URLSearchParams('q=2026-02-15T10:30')));
    expect(query.get('q')).toBe('2026-02-15T10:30');
  });
});

describe('exportHref', () => {
  it('names the format first and drops each empty filter', () => {
    expect(exportHref(none, 'csv')).toBe('/api/accounts/export?format=csv');
  });

  it('carries the filters with each time in RFC 3339', () => {
    const href = exportHref(
      { ...none, unconfirmed: 'true', created_from: '2026-02-15T10:30' },
      'userpass',
    );
    const url = new URL(href, 'http://127.0.0.1');
    expect(url.pathname).toBe('/api/accounts/export');
    expect([...url.searchParams]).toEqual([
      ['format', 'userpass'],
      ['unconfirmed', 'true'],
      ['created_from', utc(2026, 2, 15, 10, 30)],
    ]);
  });
});

describe('filterQuery', () => {
  it('holds only the filters that are set, as the form sends them', () => {
    expect(filterQuery({ proxy: 'socks5://h:1080' })).toBe('proxy=socks5%3A%2F%2Fh%3A1080');
    expect(filterQuery({ ...none, run: '2', q: '' })).toBe('run=2');
  });

  it('is empty with no filter', () => {
    expect(filterQuery({})).toBe('');
    expect(filterQuery(none)).toBe('');
  });
});

describe('hasFilters', () => {
  it('is false when no filter is set', () => {
    expect(hasFilters({})).toBe(false);
    expect(hasFilters(none)).toBe(false);
  });

  it('is true when one filter is set', () => {
    expect(hasFilters({ ...none, q: 'bob' })).toBe(true);
    expect(hasFilters({ created_to: '2026-07-01T00:05' })).toBe(true);
  });
});

describe('pageQuery', () => {
  it('keeps the set filters as the form sent them and sets before', () => {
    const query = pageQuery({ ...none, totp: 'true', created_to: '2026-07-01T00:05' }, 42);
    expect([...new URLSearchParams(query)]).toEqual([
      ['totp', 'true'],
      ['created_to', '2026-07-01T00:05'],
      ['before', '42'],
    ]);
  });

  it('holds only before with no filter', () => {
    expect(pageQuery(none, 7)).toBe('before=7');
  });
});
