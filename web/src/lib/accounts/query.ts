import type { operations } from '#lib/api.gen.d.ts';

// The filters of the accounts screen. The page query holds each one as its GET form sends it.
const filterNames = [
  'run',
  'unconfirmed',
  'totp',
  'proxy',
  'created_from',
  'created_to',
  'q',
] as const satisfies readonly (keyof NonNullable<
  operations['listAccounts']['parameters']['query']
>)[];

type FilterName = (typeof filterNames)[number];

/** The form value of each filter. The empty value selects every account. */
export type Filters = Record<FilterName, string>;

export type ExportFormat = operations['exportAccounts']['parameters']['query']['format'];

// The form sends these as datetime-local input values: a local time with no zone.
const timeFilters: ReadonlySet<FilterName> = new Set(['created_from', 'created_to']);

/** Returns the form value of each filter in the page query. A missing filter is empty. */
export function readFilters(page: URLSearchParams): Filters {
  return Object.fromEntries(filterNames.map((name) => [name, page.get(name) ?? ''])) as Filters;
}

// The server reads an RFC 3339 time, and Date reads a time with no zone as local time.
// A value that is not a time goes as it is, so the server refuses it with 400.
function toRFC3339(value: string): string {
  const time = new Date(value);
  return Number.isNaN(time.getTime()) ? value : time.toISOString();
}

// The server refuses an empty run or unconfirmed, so the API query drops each empty value.
function apiFilters(filters: Filters): URLSearchParams {
  const query = new URLSearchParams();
  for (const name of filterNames) {
    const value = filters[name];
    if (value !== '') {
      query.set(name, timeFilters.has(name) ? toRFC3339(value) : value);
    }
  }
  return query;
}

/** Returns the query of GET /api/accounts for the page query: its filters and its before. */
export function listQuery(page: URLSearchParams): string {
  const query = apiFilters(readFilters(page));
  const before = page.get('before') ?? '';
  if (before !== '') {
    query.set('before', before);
  }
  return query.toString();
}

/** Returns the export URL of the accounts that the filters select, in one format. */
export function exportHref(filters: Filters, format: ExportFormat): string {
  const query = new URLSearchParams({ format });
  for (const [name, value] of apiFilters(filters)) {
    query.set(name, value);
  }
  return `/api/accounts/export?${query.toString()}`;
}

// The page query holds each filter as the form sends it. A missing or empty filter is left out.
function setFilters(filters: Partial<Filters>): URLSearchParams {
  const query = new URLSearchParams();
  for (const name of filterNames) {
    const value = filters[name] ?? '';
    if (value !== '') {
      query.set(name, value);
    }
  }
  return query;
}

/** Returns the page query of the accounts screen for the filters that are set. */
export function filterQuery(filters: Partial<Filters>): string {
  return setFilters(filters).toString();
}

/** Reports whether any of the given filters is set. A missing filter is not set. */
export function hasFilters(filters: Partial<Filters>): boolean {
  return setFilters(filters).size > 0;
}

/** Returns the page query of the next page: the filters that are set, and before. */
export function pageQuery(filters: Filters, before: number): string {
  const query = setFilters(filters);
  query.set('before', String(before));
  return query.toString();
}
