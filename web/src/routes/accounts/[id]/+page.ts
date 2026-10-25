import { error } from '@sveltejs/kit';
import { apiClient } from '#lib/api.ts';
import type { PageLoad } from './$types';

export const load: PageLoad = async ({ fetch, params }) => {
  // An id that is not a number goes as NaN, so the server refuses it with 400.
  const { data, response } = await apiClient(fetch).GET('/api/accounts/{id}', {
    params: { path: { id: Number(params.id) } },
  });
  if (data === undefined) {
    error(response.status, `Account read failed with HTTP ${String(response.status)}`);
  }
  return { account: data };
};
