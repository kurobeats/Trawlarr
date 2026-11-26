-- Privacy-preserving capture: record which fields were filled, not their values.
ALTER TABLE pages ADD COLUMN capture_values INTEGER NOT NULL DEFAULT 0;
ALTER TABLE campaign_targets ADD COLUMN capture_fields TEXT NOT NULL DEFAULT '[]';

-- Delivery states distinguish "never sent" from "may have been sent".
-- (status gains 'uncertain'; no schema change needed for TEXT.)

-- Sender profile test history, so a launch can be gated on a proven relay.
ALTER TABLE profiles ADD COLUMN last_test_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE profiles ADD COLUMN last_test_error TEXT NOT NULL DEFAULT '';
