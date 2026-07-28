"""Shared Postgres connection for matcher batch jobs.

Connects via the Supabase transaction pooler (DB_URL from .env).
pgvector registered so embed.py can write vector(384) columns directly.
"""
import os
from dotenv import load_dotenv
import psycopg
from pgvector.psycopg import register_vector

load_dotenv(dotenv_path=os.path.join(os.path.dirname(__file__), "..", "..", ".env"))


def get_conn() -> psycopg.Connection:
    conn = psycopg.connect(os.environ["DB_URL"])
    register_vector(conn)
    return conn
