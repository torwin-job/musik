"""Errors raised by the plugin helper."""

from __future__ import annotations


CODE_INVALID_REQUEST = "invalid_request"
CODE_PROTOCOL_MISMATCH = "protocol_mismatch"
CODE_UNSUPPORTED_ACTION = "unsupported_action"
CODE_UNSUPPORTED_URL = "unsupported_url"
CODE_DOWNLOAD_FAILED = "download_failed"
CODE_WRITE_FAILED = "write_failed"
CODE_CANCELLED = "cancelled"
CODE_INTERNAL = "internal"

KNOWN_ERROR_CODES = frozenset(
    {
        CODE_INVALID_REQUEST,
        CODE_PROTOCOL_MISMATCH,
        CODE_UNSUPPORTED_ACTION,
        CODE_UNSUPPORTED_URL,
        CODE_DOWNLOAD_FAILED,
        CODE_WRITE_FAILED,
        CODE_CANCELLED,
        CODE_INTERNAL,
    }
)


class PluginError(Exception):
    """A protocol failure that becomes one error message on stdout."""

    def __init__(self, code: str, message: str) -> None:
        self.code = code
        self.message = message
        super().__init__(message)


class ManifestError(Exception):
    """A manifest or discovery document failed validation."""


class PluginConfigError(Exception):
    """The plugin class itself is inconsistent with protocol v1."""
