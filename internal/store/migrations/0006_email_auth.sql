-- A stored config from before the OAuth2 login logs in with a password. It gets
-- the auth and the empty oauth2 section that the API form now holds.
UPDATE runs
SET config = json_set(config,
    '$.email.auth', 'password',
    '$.email.oauth2', json('{"client_id":"","client_secret_set":false,"refresh_token_set":false,"token_url":""}'))
WHERE json_type(config, '$.email.auth') IS NULL;

UPDATE presets
SET config = json_set(config,
    '$.email.auth', 'password',
    '$.email.oauth2', json('{"client_id":"","client_secret_set":false,"refresh_token_set":false,"token_url":""}'))
WHERE json_type(config, '$.email.auth') IS NULL;

PRAGMA user_version = 6;
