-- The one domain of a stored config becomes the one item of its domain list.
UPDATE runs
SET config = json_remove(
    json_set(config, '$.email.imap_domains', json_array(json_extract(config, '$.email.imap_domain'))),
    '$.email.imap_domain')
WHERE json_type(config, '$.email.imap_domain') = 'text';

UPDATE presets
SET config = json_remove(
    json_set(config, '$.email.imap_domains', json_array(json_extract(config, '$.email.imap_domain'))),
    '$.email.imap_domain')
WHERE json_type(config, '$.email.imap_domain') = 'text';

PRAGMA user_version = 5;
