-- GeoIP annotation of the first interaction, resolved locally from a database
-- the operator installs. Coordinates only: no third-party lookup is made.
ALTER TABLE campaign_targets ADD COLUMN latitude REAL NOT NULL DEFAULT 0;
ALTER TABLE campaign_targets ADD COLUMN longitude REAL NOT NULL DEFAULT 0;
ALTER TABLE campaign_targets ADD COLUMN city TEXT NOT NULL DEFAULT '';
ALTER TABLE campaign_targets ADD COLUMN country TEXT NOT NULL DEFAULT '';
