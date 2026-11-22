# backend/utils/db_pool.py
"""
Cleanly disposes the SQLAlchemy engine connection pool on shutdown.
Prevents Postgres lock leaks and "too many connections" errors on restart.
"""
import logging
from models.db import engine

logger = logging.getLogger(__name__)


def drain_db_connections():
    """
    Dispose all connections in the pool.
    Any in-use connections finish their current transaction first,
    then are closed when returned to the pool.
    """
    try:
        engine.dispose()
        logger.info("SQLAlchemy connection pool disposed cleanly")
    except Exception as e:
        logger.error(f"Error disposing connection pool: {e}")
