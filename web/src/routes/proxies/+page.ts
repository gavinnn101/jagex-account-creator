import { error } from '@sveltejs/kit';
import { apiClient } from '#lib/api.ts';
import type { PageLoad } from './$types';

export const load: PageLoad = async ({ fetch }) => {
  const { data, response } = await apiClient(fetch).GET('/api/proxies');
  if (data === undefined) {
    error(response.status, `Proxy read failed with HTTP ${String(response.status)}`);
  }
  return { proxies: data.proxies };
};
