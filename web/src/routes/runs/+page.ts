import { error } from '@sveltejs/kit';
import { apiClient } from '#lib/api.ts';
import type { PageLoad } from './$types';

export const load: PageLoad = async ({ fetch, url }) => {
  // The page query holds text, so a bad before goes as it is and the server refuses it with 400.
  const before = url.searchParams.get('before') ?? '';
  const { data, response } = await apiClient(fetch).GET('/api/runs', {
    querySerializer: () => (before === '' ? '' : new URLSearchParams({ before }).toString()),
  });
  if (data === undefined) {
    error(response.status, `Run read failed with HTTP ${String(response.status)}`);
  }
  return { page: data };
};
