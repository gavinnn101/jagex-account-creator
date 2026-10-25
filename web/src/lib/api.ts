import createClient from 'openapi-fetch';
import type { components, paths } from '#lib/api.gen.d.ts';

/** The error answer of a request: an Error, or the ValidationResult of a refused config. */
export type ApiFailure = components['schemas']['Error'] | components['schemas']['ValidationResult'];

/** Returns a client of the serve API that sends each request through fetch. */
export function apiClient(fetch: typeof globalThis.fetch) {
  return createClient<paths>({ fetch });
}

/** Returns the messages of an error answer: each error of a refused config, or the one message. */
export function errorMessages(error: ApiFailure): string[] {
  return 'errors' in error ? error.errors : [error.message];
}
