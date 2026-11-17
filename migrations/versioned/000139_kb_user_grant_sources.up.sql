CREATE TABLE IF NOT EXISTS kb_user_grant_sources (
    knowledge_base_id VARCHAR(36) NOT NULL,
    recipient_user_id VARCHAR(36) NOT NULL,
    source VARCHAR(96) NOT NULL,
    permission VARCHAR(32) NOT NULL,
    granted_by_user_id VARCHAR(36) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (knowledge_base_id, recipient_user_id, source),
    CHECK (permission IN ('viewer', 'editor'))
);
CREATE INDEX IF NOT EXISTS idx_kb_user_grant_sources_recipient ON kb_user_grant_sources(recipient_user_id);
