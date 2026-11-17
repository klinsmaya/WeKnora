CREATE TABLE IF NOT EXISTS kb_user_grants (
    knowledge_base_id VARCHAR(36) NOT NULL,
    recipient_user_id VARCHAR(36) NOT NULL,
    permission VARCHAR(32) NOT NULL,
    granted_by_user_id VARCHAR(36) NOT NULL,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    PRIMARY KEY (knowledge_base_id, recipient_user_id),
    CHECK (permission IN ('viewer', 'editor'))
);
CREATE INDEX IF NOT EXISTS idx_kb_user_grants_recipient_user_id ON kb_user_grants(recipient_user_id);
