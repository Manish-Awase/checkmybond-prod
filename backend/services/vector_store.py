# backend/services/vector_store.py
import uuid
from typing import List, Dict, Any
from qdrant_client import QdrantClient
from qdrant_client.models import (
    Distance, VectorParams, PointStruct,
    Filter, FieldCondition, MatchValue
)
from sentence_transformers import SentenceTransformer

class VectorStoreService:
    """
    Implements Vector Indexing & Retrieval Layer from mind map:
    - Embedding generation (SentenceTransformer)
    - Namespace isolation (per-document Qdrant collections)
    - Persistent vector store (Qdrant, not in-memory FAISS)
    - Top-k retrieval with metadata filters
    - Diagnostics logging (point_id returned with every result)

    Critical design: each document = its own Qdrant collection.
    This is namespace isolation — tenant A's vectors never pollute tenant B.
    Also enables fast filtered search (only this document's chunks).
    """

    EMBEDDING_MODEL = "all-MiniLM-L6-v2"   # 384-dim, fast, good for financial text
    VECTOR_DIM      = 384

    def __init__(self, host: str = "localhost", port: int = 6333):
        self.client = QdrantClient(host=host, port=port)
        self.model  = SentenceTransformer(self.EMBEDDING_MODEL)

    def _collection_name(self, doc_hash: str) -> str:
        """Namespace isolation: each document gets a unique collection."""
        return f"doc_{doc_hash[:20]}"

    def ensure_collection(self, doc_hash: str):
        """Create collection if it doesn't exist (idempotent)."""
        name = self._collection_name(doc_hash)
        existing = {c.name for c in self.client.get_collections().collections}
        if name not in existing:
            self.client.create_collection(
                collection_name=name,
                vectors_config=VectorParams(size=self.VECTOR_DIM, distance=Distance.COSINE)
            )
        return name

    def index_chunks(self, chunks: List[Dict[str, Any]], doc_hash: str) -> int:
        """
        Embed all chunks and store in Qdrant with full metadata payload.

        Key: payload stored WITH vector → retrieval returns text + metadata
        in one query. No separate DB lookup needed.
        """
        if not chunks:
            return 0

        collection = self.ensure_collection(doc_hash)
        texts   = [c["text"] for c in chunks]
        vectors = self.model.encode(texts, batch_size=32, show_progress_bar=False).tolist()

        points = [
            PointStruct(
                id      = str(uuid.uuid4()),
                vector  = vec,
                payload = {
                    "text":       chunk["text"],
                    "page_num":   chunk["page_num"],
                    "section":    chunk["section"],
                    "chunk_type": chunk["chunk_type"],
                    "doc_hash":   chunk["doc_hash"],
                    "chunk_id":   chunk["chunk_id"],
                    "table_type": chunk.get("table_type"),
                    "headers":    chunk.get("headers", []),
                }
            )
            for vec, chunk in zip(vectors, chunks)
        ]

        # Upsert in batches of 100
        batch_size = 100
        for i in range(0, len(points), batch_size):
            self.client.upsert(collection_name=collection, points=points[i:i+batch_size])

        return len(points)

    def retrieve(self, query: str, doc_hash: str, top_k: int = 3) -> List[Dict[str, Any]]:
        """
        Top-k similarity search within document namespace.
        Returns chunks WITH scores and point_ids for diagnostics logging.

        Diagnostics: every retrieved chunk's point_id is stored in the
        Correction record → you can trace exactly which chunks were used
        for any extraction, ever.
        """
        collection = self._collection_name(doc_hash)
        query_vec  = self.model.encode([query])[0].tolist()

        try:
            results = self.client.search(
                collection_name=collection,
                query_vector=query_vec,
                limit=top_k,
                with_payload=True,
                with_vectors=False,
            )
        except Exception:
            return []

        return [
            {
                **r.payload,
                "retrieval_score": round(r.score, 4),
                "point_id":        str(r.id),   # for diagnostics logging
            }
            for r in results
        ]

    def delete_document(self, doc_hash: str):
        """Clean up document namespace (GDPR compliance)."""
        try:
            self.client.delete_collection(self._collection_name(doc_hash))
        except Exception:
            pass