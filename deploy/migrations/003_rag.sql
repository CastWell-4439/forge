-- Phase AE-3: RAG hybrid search schema
-- Requires PostgreSQL with pgvector extension.
--
-- Stock PostgreSQL does not ship pgvector, and this whole schema is
-- optional: the production RAG store is file-based (internal/agent/rag),
-- and documents/ serves a future pgvector backend. So the DDL runs inside a
-- DO block that catches exactly the missing-extension failure
-- (SQLSTATE 0A000, feature_not_supported) and skips with a NOTICE instead
-- of stopping startup. Every other SQL error still fails loudly — only the
-- unavailable extension is swallowed.

DO $$
BEGIN
    CREATE EXTENSION IF NOT EXISTS vector;

    CREATE TABLE IF NOT EXISTS documents (
        id          TEXT PRIMARY KEY,
        content     TEXT NOT NULL,
        metadata    JSONB DEFAULT '{}',
        embedding   vector(1536),
        category    TEXT DEFAULT 'knowledge',
        created_at  TIMESTAMPTZ DEFAULT now(),
        updated_at  TIMESTAMPTZ DEFAULT now()
    );

    -- Vector index (IVFFlat for cosine similarity)
    CREATE INDEX IF NOT EXISTS idx_documents_embedding
        ON documents USING ivfflat (embedding vector_cosine_ops) WITH (lists = 100);

    -- Full-text search index
    CREATE INDEX IF NOT EXISTS idx_documents_content_fts
        ON documents USING gin (to_tsvector('english', content));

    -- Category index for filtered queries
    CREATE INDEX IF NOT EXISTS idx_documents_category
        ON documents (category);
EXCEPTION
    WHEN feature_not_supported THEN
        RAISE NOTICE 'rag schema skipped: pgvector extension unavailable';
END $$;
