-- Trial clock starts at the first sign-in, not at approval.
--   access_hours  granted hours not started yet (approved, never signed in). The first
--                 authenticated request sets access_expires_at = NOW() + access_hours and clears it.
ALTER TABLE admins ADD COLUMN IF NOT EXISTS access_hours DOUBLE PRECISION;
