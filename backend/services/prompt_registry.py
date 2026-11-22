# backend/services/prompt_registry_service.py
import hashlib
from sqlalchemy.orm import Session
from models.db import PromptVersion

# Default extraction prompt templates
DEFAULT_PROMPTS = {
    "field_extraction_v1": """Extract the value for the following field from the document context.

Field name: {field_name}
Field type: {field_type}
Known aliases: {aliases}
Description: {description}

Document context:
{context}

Instructions:
1. Extract ONLY from the provided context
2. If not found, return NOT FOUND
3. Return the raw value without units unless part of the value
4. Match the expected format for {field_type}

<FINAL_VALUE>extracted value or NOT FOUND</FINAL_VALUE>""",

    "field_extraction_v2": """You are a precise financial document extraction system.

FIELD: {field_name}
TYPE:  {field_type}
ALIASES: {aliases}
DESCRIPTION: {description}

DOCUMENT CONTEXT:
{context}

EXTRACTION RULES:
- Extract ONLY what is explicitly stated in the context above
- Do NOT infer, estimate, or calculate values
- If field_name or any alias appears multiple times, extract the PRIMARY value
- Format: match the {field_type} format exactly
- If not found in context: return NOT FOUND (not null, not empty, not 0)

Think step by step:
1. Look for {field_name} and aliases: {aliases}
2. Find the associated value
3. Verify it matches {field_type} format

<FINAL_VALUE>extracted value or NOT FOUND</FINAL_VALUE>""",
}

class PromptRegistryService:
    """
    Prompt versioning — every prompt stored with a content hash.
    Every extraction linked to exact prompt_id used.

    Why this matters: if accuracy drops after a prompt change,
    you can trace exactly which prompt caused it and roll back.
    """

    def __init__(self, db: Session):
        self.db = db
        self._seed_defaults()

    def _seed_defaults(self):
        """Ensure default prompts exist on startup."""
        for name, template in DEFAULT_PROMPTS.items():
            self.register(name, template)

    def register(self, prompt_name: str, template_text: str) -> str:
        """Register prompt with content hash. Returns prompt_id."""
        content_hash = hashlib.sha256(template_text.encode()).hexdigest()[:8]
        prompt_id    = f"{prompt_name}:{content_hash}"

        existing = self.db.query(PromptVersion).filter_by(prompt_id=prompt_id).first()
        if not existing:
            record = PromptVersion(
                prompt_id     = prompt_id,
                prompt_name   = prompt_name.rsplit("_v", 1)[0] if "_v" in prompt_name else prompt_name,
                version_tag       = "v" + prompt_name.rsplit("_v", 1)[-1] if "_v" in prompt_name else "v1",
                body = template_text,
                is_active     = True,
            )
            self.db.add(record)
            self.db.commit()
        return prompt_id

    def get_active(self, prompt_name: str = "field_extraction") -> tuple[str, str]:
        """Returns (prompt_id, template_text) for the active prompt."""
        record = (
            self.db.query(PromptVersion)
            .filter_by(prompt_name=prompt_name, is_active=True)
            .order_by(PromptVersion.created_at.desc())
            .first()
        )
        if not record:
            # Fallback to v2 default
            template = DEFAULT_PROMPTS["field_extraction_v2"]
            prompt_id = self.register("field_extraction_v2", template)
            return prompt_id, template
        return record.prompt_id, record.body

    def format_prompt(self, template: str, field, context: str) -> str:
        """Fill prompt template with field definition and retrieved context."""
        return template.format(
            field_name  = field.name,
            field_type  = field.field_type.value,
            aliases     = ", ".join(field.aliases) if field.aliases else "none",
            description = field.description or "not specified",
            context     = context,
        )