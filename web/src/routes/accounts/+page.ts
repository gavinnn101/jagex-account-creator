import { error } from '@sveltejs/kit';
import { listQuery, readFilters } from '#lib/accounts/query.ts';
import { apiClient } from '#lib/api.ts';
import type { PageLoad } from './$types';

export const load: PageLoad = async ({ fetch, url }) => {
  const query = listQuery(url.searchParams);
  // The page query holds text, so the query module builds the query and the server checks each value.
  const { data, response } = await apiClient(fetch).GET('/api/accounts', {
    querySerializer: () => query,
  });
  if (data === undefined) {
    error(response.status, `Account read failed with HTTP ${String(response.status)}`);
  }
  return { page: data, filters: readFilters(url.searchParams) };
};
