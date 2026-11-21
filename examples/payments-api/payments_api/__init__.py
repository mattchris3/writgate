"""Mock payments API, the demo target for writgate."""

from .app import app_from_env, create_app

__all__ = ["app_from_env", "create_app"]
