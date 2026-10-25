import { error } from '@sveltejs/kit';
import { apiClient } from '#lib/api.ts';
import type { PageLoad } from './$types';

export const load: PageLoad = async ({ fetch }) => {
  const client = apiClient(fetch);
  const [config, presets] = await Promise.all([
    client.GET('/api/config'),
    client.GET('/api/presets'),
  ]);
  if (config.data === undefined) {
    const { status } = config.response;
    error(status, `Config read failed with HTTP ${String(status)}`);
  }
  if (presets.data === undefined) {
    const { status } = presets.response;
    error(status, `Preset read failed with HTTP ${String(status)}`);
  }
  return { config: config.data, presets: presets.data.presets };
};
