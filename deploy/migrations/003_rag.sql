-- Phase AE-3: RAG hybrid search schema
-- Requires PostgreSQL with pgvector extension.

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
