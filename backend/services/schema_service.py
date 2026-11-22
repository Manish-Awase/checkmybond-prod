# backend/services/schema_service.py
import hashlib, json, re
from enum import Enum
from typing import List, Optional
from pydantic import BaseModel, field_validator
from sqlalchemy.orm import Session
from models.db import SchemaRegistry
from services.schema_models import FieldType, FieldDefinition, SchemaDefinition, FIELD_VALIDATORS


class SchemaServiceImpl:
    def __init__(self, db: Session):
        self.db = db

    def register(self, doc_schema: SchemaDefinition) -> str:
        """
        Hash schema for immutable versioning.
        Same schema content = same hash = no duplicate registration.
        """
        schema_json = doc_schema.model_dump_json(indent=None)
        schema_hash = hashlib.sha256(schema_json.encode()).hexdigest()[:12]
        schema_id   = f"{doc_schema.schema_name}:{doc_schema.version}:{schema_hash}"

        existing = self.db.query(SchemaRegistry).filter_by(schema_id=schema_id).first()
        if not existing:
            record = SchemaRegistry(
                schema_id       = schema_id,
                schema_name     = doc_schema.schema_name,
                version         = doc_schema.version,
                definition_json = schema_json,
            )
            self.db.add(record)
            self.db.commit()
        return schema_id

    def get(self, schema_id: str) -> SchemaDefinition:
        row = self.db.query(SchemaRegistry).filter_by(schema_id=schema_id).first()
        if not row:
            raise ValueError(f"Schema {schema_id} not found")
        return SchemaDefinition.model_validate_json(row.definition_json)

    def list_all(self) -> List[dict]:
        rows = self.db.query(SchemaRegistry).order_by(SchemaRegistry.created_at.desc()).all()
        return [{"schema_id": r.schema_id, "name": r.schema_name, "version": r.version,
                 "created_at": str(r.created_at)} for r in rows]

    def validate_value(self, value: str, field: FieldDefinition) -> tuple[bool, str]:
        """
        Type-aware validation. Applied AFTER LLM extraction, BEFORE confidence scoring.
        Returns (is_valid, reason).
        """
        if not value or value.strip() in ["", "NOT FOUND", "N/A", "EXTRACTION_FAILED"]:
            if field.required:
                return False, "required_field_empty"
            return True, "optional_field_empty"

        pattern = FIELD_VALIDATORS.get(field.field_type)
        if pattern and not re.search(pattern, value.strip(), re.IGNORECASE):
            return False, f"value_does_not_match_{field.field_type}_format"

        return True, "valid"

    def build_query(self, field: FieldDefinition) -> str:
        """
        Build retrieval query incorporating field name + all aliases.
        This ensures vector search finds chunks that use any synonym.
        e.g. "Revenue Net Sales Total Revenue Gross Revenue"
        """
        parts = [field.name] + field.aliases
        if field.description:
            parts.append(field.description)
        return " ".join(parts)