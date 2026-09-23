-- 0043: bind an /authorize request to the first signed-in web user who opens
-- its consent page. GET /authorize starts a request before anyone signs in, so
-- the request has no owner; without a binding any signed-in user holding the
-- 256-bit handle could deny (cancel) it. The first preview, approval or denial
-- binds the request to that user's org and subject, and only that user may
-- decide it afterwards. Both columns are NULL until bound, and set together.

ALTER TABLE acr.oauth_authorization_requests
    ADD COLUMN IF NOT EXISTS bound_org_id TEXT,
    ADD COLUMN IF NOT EXISTS bound_subject TEXT;

ALTER TABLE acr.oauth_authorization_requests
    DROP CONSTRAINT IF EXISTS oauth_authorization_requests_bound_user_check;
ALTER TABLE acr.oauth_authorization_requests
    ADD CONSTRAINT oauth_authorization_requests_bound_user_check
    CHECK ((bound_org_id IS NULL) = (bound_subject IS NULL));

COMMENT ON COLUMN acr.oauth_authorization_requests.bound_subject IS
    'Web-assertion subject of the first signed-in user who opened this request''s consent page; only that user (bound_org_id, bound_subject) may approve or deny it. NULL until bound.';
