"""Stateless document capability with injected, currently authorized object access."""

import hashlib
import re
import threading
import time
from collections.abc import Callable
from dataclasses import dataclass
from typing import Protocol
from uuid import UUID

from agentos_compute.documents import DOCX, MAX_INPUT, PDF, extract

CAPABILITY = "document.extract.v1"
OUTPUT_MEDIA = "application/vnd.agentos.document-text+json"


class WorkerError(ValueError):
    """Stable failure code; never embeds grants, document content or adapter errors."""


@dataclass(frozen=True)
class Execution:
    tenant_id: str
    task_id: str
    step_id: str
    execution_revision: str
    lease_generation: int
    request_id: str
    trace_id: str
    deadline_unix_ms: int
    capability_version: str


@dataclass(frozen=True)
class Reference:
    id: str
    sha256: str
    size_bytes: int
    media_type: str


@dataclass(frozen=True)
class Request:
    execution: Execution
    input: Reference
    input_grant: str
    output_grant: str


@dataclass(frozen=True)
class Result:
    execution: Execution
    input_sha256: str
    output: Reference


class ObjectAccess(Protocol):
    """Trusted adapter; each call must enforce scope, expiry and current revocation.

    Grants bind the entire execution plus exact input reference/output capability.
    read enforces its byte limit before allocation. stage is immutable/idempotent
    per execution request and digest. Staged output is never a business commit;
    the task owner verifies current lease/authorization and rehashes before commit.
    Uncommitted output expires under object-store retention policy.
    """

    def authorize(self, request: Request) -> None: ...

    def read(self, request: Request, limit: int, check: Callable[[], None]) -> bytes: ...

    def stage(self, request: Request, data: bytes, check: Callable[[], None]) -> Reference: ...


def _token(value: str, limit: int = 256) -> bool:
    return bool(re.fullmatch(r"[A-Za-z0-9_.:-]{1," + str(limit) + r"}", value))


def _digest(value: str) -> bool:
    return bool(re.fullmatch(r"[0-9a-f]{64}", value))


def _validate(request: Request, now: float) -> None:
    execution = request.execution
    try:
        for value in (execution.tenant_id, execution.task_id, execution.request_id):
            if str(UUID(value)) != value or UUID(value).int == 0:
                raise ValueError("invalid UUID")
    except ValueError as error:
        raise WorkerError("invalid_execution") from error
    if (
        not _token(execution.step_id)
        or not _token(execution.trace_id)
        or not _digest(execution.execution_revision)
        or not 0 < execution.lease_generation < 2**64
        or execution.capability_version != CAPABILITY
        or execution.deadline_unix_ms <= now * 1000
        or execution.deadline_unix_ms > (now + 30) * 1000
    ):
        raise WorkerError("invalid_execution")
    if (
        not _token(request.input.id)
        or not _digest(request.input.sha256)
        or not 0 < request.input.size_bytes <= MAX_INPUT
        or request.input.media_type not in (PDF, DOCX)
        or not _token(request.input_grant, 4096)
        or not _token(request.output_grant, 4096)
    ):
        raise WorkerError("invalid_object_request")


class DocumentWorker:
    def __init__(self, objects: ObjectAccess, concurrency: int = 2) -> None:
        if not 1 <= concurrency <= 4:
            raise ValueError("concurrency must be between 1 and 4")
        self._objects = objects
        self._slots = threading.BoundedSemaphore(concurrency)

    def extract(self, request: Request, check_rpc: Callable[[], None]) -> Result:
        now = time.time()
        _validate(request, now)
        deadline = time.monotonic() + (request.execution.deadline_unix_ms / 1000 - now)

        def check() -> None:
            check_rpc()
            if time.monotonic() >= deadline:
                raise WorkerError("deadline_exceeded")

        check()
        if not self._slots.acquire(blocking=False):
            raise WorkerError("worker_busy")
        try:
            self._objects.authorize(request)
            data = self._objects.read(request, MAX_INPUT, check)
            check()
            if (
                len(data) != request.input.size_bytes
                or hashlib.sha256(data).hexdigest() != request.input.sha256
            ):
                raise WorkerError("input_digest_mismatch")
            payload = extract(data, request.input.media_type, check)
            check()
            self._objects.authorize(request)
            output = self._objects.stage(request, payload, check)
            check()
            self._objects.authorize(request)
            if (
                not _token(output.id)
                or output.sha256 != hashlib.sha256(payload).hexdigest()
                or output.size_bytes != len(payload)
                or output.media_type != OUTPUT_MEDIA
            ):
                raise WorkerError("output_receipt_mismatch")
            check()
            return Result(request.execution, request.input.sha256, output)
        finally:
            self._slots.release()
