# backend/services/schema_models.py
"""
Pure Pydantic schema models — no SQLAlchemy, no Postgres, no Redis.
Imported by both the server (schema_service.py) and the desktop CLI.
"""
from enum import Enum
from typing import List, Optional
from pydantic import BaseModel, field_validator


class FieldType(str, Enum):
    CURRENCY   = "currency"
    DATE       = "date"
    PERCENTAGE = "percentage"
    EIN        = "ein"
    TEXT       = "text"
    NUMBER     = "number"
    BOOLEAN    = "boolean"


# Type-specific regex validators (moved here so desktop can use them too)
FIELD_VALIDATORS = {
    FieldType.CURRENCY:   r"^\$?[\d,]+(\.\d{2})?([MBKmb])?$|^[\d,]+(\.\d+)?$",
    FieldType.PERCENTAGE: r"^\d{1,3}(\.\d+)?%?$",
    FieldType.EIN:        r"^\d{2}-\d{7}$",
    FieldType.DATE:       r"\d{4}[-/]\d{2}[-/]\d{2}|\d{2}[-/]\d{2}[-/]\d{4}",
    FieldType.NUMBER:     r"^[\d,]+(\.\d+)?$",
}


class FieldDefinition(BaseModel):
    """
    Single field in a user-defined schema.
    Identical to the original in schema_service.py — just moved here.
    """
    name:        str
    field_type:  FieldType
    aliases:     List[str] = []
    required:    bool = True
    description: Optional[str] = None
    region:      Optional[dict] = None

    @field_validator("name")
    @classmethod
    def name_must_not_be_empty(cls, v):
        if not v.strip():
            raise ValueError("Field name cannot be empty")
        return v.strip()


class SchemaDefinition(BaseModel):
    schema_name: str
    version:     str
    fields:      List[FieldDefinition]
    mode: str = "llm"
