import { describe, expect, it } from 'vitest';
import { errorMessages } from './api.ts';

describe('errorMessages', () => {
  it('returns each error of a refused config', () => {
    expect(
      errorMessages({ valid: false, errors: ['email.imap_host is required', 'bad timeout'] }),
    ).toEqual(['email.imap_host is required', 'bad timeout']);
  });

  it('returns no message for a refused config with no error', () => {
    expect(errorMessages({ valid: false, errors: [] })).toEqual([]);
  });

  it('returns the one message of an Error', () => {
    expect(errorMessages({ message: 'preset 4 not found' })).toEqual(['preset 4 not found']);
  });
});
