 # backend/services/normalizer.py
import re, hashlib
from typing import List, Dict, Any
from services.table_tagger import TableTagger

class Normalizer:
    """
    Implements 'Content Normalization Layer' from the mind map:
    - Header-aware serialization (section detection)
    - Table type tagging (via TableTagger)
    - Section-aware chunking (split on semantic sections, not char count)
    - Metadata enrichment (page_num, section, chunk_type, doc_hash per chunk)

    The key insight from Codebasics:
    NAIVE:  Split every 500 characters → breaks financial context
    BETTER: Split by page boundary → preserves context but coarse
    BEST:   Split by semantic section (header-aware chunking) → used here
    """

    SECTION_HEADER_PATTERN = re.compile(
        r"^(#{1,6}\s+.+|[A-Z][A-Z\s]{4,50}$|\d+\.\s+[A-Z].+|"
        r"ITEM\s+\d+|NOTE\s+\d+|PART\s+[IVX]+)",
        re.MULTILINE
    )

    def __init__(self):
        self.tagger = TableTagger()

    def chunk_document(self, parsed_doc: dict) -> List[Dict[str, Any]]:
        """
        Master chunking function. Returns list of chunks ready for embedding.
        Each chunk has full metadata for retrieval and provenance.
        """
        chunks = []
        for page in parsed_doc["pages"]:
            # Chunk 1: prose sections from page text
            prose_chunks = self._chunk_prose(page)
            chunks.extend(prose_chunks)

            # Chunk 2: tables from page (indexed separately)
            # Key insight from Codebasics: tables have different semantic density
            # than prose → index separately for better retrieval
            table_chunks = self._chunk_tables(page)
            chunks.extend(table_chunks)

        return chunks

    def _chunk_prose(self, page: dict) -> List[Dict[str, Any]]:
        """Split prose text on detected section headers."""
        text = page.get("text", "")
        if not text.strip():
            return []

        # Find section header positions
        splits = [(m.start(), m.group()) for m in self.SECTION_HEADER_PATTERN.finditer(text)]

        if not splits:
            # No headers detected — treat whole page as one chunk
            return [self._make_chunk(
                text=text.strip(),
                page_num=page["page_num"],
                section="page_" + str(page["page_num"]),
                chunk_type="prose",
                doc_hash=page["doc_hash"],
            )]

        # Split into sections
        chunks = []
        for i, (pos, header) in enumerate(splits):
            end = splits[i+1][0] if i+1 < len(splits) else len(text)
            section_text = text[pos:end].strip()
            if len(section_text) > 50:  # skip trivially short sections
                chunks.append(self._make_chunk(
                    text=section_text,
                    page_num=page["page_num"],
                    section=header.strip()[:80],
                    chunk_type="prose",
                    doc_hash=page["doc_hash"],
                ))
        return chunks

    def _chunk_tables(self, page: dict) -> List[Dict[str, Any]]:
        """Process and serialize each table on the page."""
        chunks = []
        for table_idx, table_rows in enumerate(page.get("tables", [])):
            if not table_rows:
                continue

            headers, data_rows = self.tagger.get_column_headers(table_rows)
            surrounding = page.get("text", "")[:300]  # context for classification
            table_type  = self.tagger.classify(table_rows, surrounding)
            serialized  = self.tagger.serialize(data_rows, headers, table_type)

            if serialized.strip():
                chunks.append(self._make_chunk(
                    text=serialized,
                    page_num=page["page_num"],
                    section=f"table_{table_idx+1}_page_{page['page_num']}",
                    chunk_type="table",
                    doc_hash=page["doc_hash"],
                    table_type=table_type.value,
                    headers=headers,
                ))
        return chunks

    def _make_chunk(self, text: str, page_num: int, section: str,
                    chunk_type: str, doc_hash: str, **extra) -> Dict[str, Any]:
        """
        Metadata enrichment: every chunk carries full provenance.
        chunk_id is deterministic hash — same content = same ID.
        """
        chunk_id = hashlib.sha256(f"{doc_hash}:{section}:{text[:100]}".encode()).hexdigest()[:16]
        return {
            "chunk_id":   chunk_id,
            "text":       text,
            "page_num":   page_num,
            "section":    section,
            "chunk_type": chunk_type,
            "doc_hash":   doc_hash,
            **extra
        }