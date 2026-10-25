import { getContext, setContext } from 'svelte';
import type { components } from '#lib/api.gen.d.ts';

type Config = components['schemas']['Config'];

/** The context key of the edited config. A component test passes its config under this key. */
export const editedConfigKey = Symbol();

/**
 * Gives config, the state copy of the saved config that the run screen edits, to the
 * components below the caller. Call it at component start, before a child reads it.
 */
export function provideEditedConfig(config: Config): void {
  setContext(editedConfigKey, config);
}

/**
 * Returns the edited config of the run screen. Call it at component start.
 * Limitation: a reader renders only below a provider. With no provider, the render throws,
 * and never gets a silent default.
 */
export function editedConfig(): Config {
  const config = getContext<Config | undefined>(editedConfigKey);
  if (config === undefined) {
    throw new Error('no edited config: render this component below provideEditedConfig');
  }
  return config;
}
