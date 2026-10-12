-- Trial access: self-service signups wait for approval, then get a time-boxed account.
--   status            pending (signed up, not approved) | active
--   access_expires_at NULL = no limit (existing accounts, superadmins); past = login refused
ALTER TABLE admins ADD COLUMN IF NOT EXISTS status VARCHAR(20) NOT NULL DEFAULT 'active';
ALTER TABLE admins ADD COLUMN IF NOT EXISTS access_expires_at TIMESTAMPTZ;
ALTER TABLE admins ADD COLUMN IF NOT EXISTS signup_note TEXT NOT NULL DEFAULT '';
DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'admins_status_check') THEN
        ALTER TABLE admins ADD CONSTRAINT admins_status_check CHECK (status IN ('pending', 'active'));
    END IF;
END $$;
