import { error } from '@sveltejs/kit';
import { apiClient } from '#lib/api.ts';
import type { PageLoad } from './$types';

export const load: PageLoad = async ({ fetch }) => {
  const client = apiClient(fetch);
  const [check, config] = await Promise.all([
    client.GET('/api/checks/chrome'),
    client.GET('/api/config'),
  ]);
  if (check.data === undefined) {
    const { status } = check.response;
    error(status, `Chrome check failed with HTTP ${String(status)}`);
  }
  if (config.data === undefined) {
    const { status } = config.response;
    error(status, `Config read failed with HTTP ${String(status)}`);
  }
  return { check: check.data, config: config.data };
};
