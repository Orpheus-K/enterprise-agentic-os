import hashlib
import io
import time
from collections.abc import Callable
from concurrent.futures import ThreadPoolExecutor
from dataclasses import asdict, replace
from uuid import uuid4

import grpc
import pytest
from docx import Document

from agentos_compute.documents import DOCX
from agentos_compute.generated import compute_pb2 as wire
from agentos_compute.generated import compute_pb2_grpc
from agentos_compute.rpc import SERVER_OPTIONS, register
from agentos_compute.worker import (
    CAPABILITY,
    OUTPUT_MEDIA,
    DocumentWorker,
    Execution,
    Reference,
    Request,
    WorkerError,
)


class Objects:
    def __init__(self) -> None:
        document = Document()
        document.add_paragraph("合成资料")
        stream = io.BytesIO()
        document.save(stream)
        self.data = stream.getvalue()
        self.request = Request(
            Execution(
                str(uuid4()),
                str(uuid4()),
                "extract",
                "a" * 64,
                1,
                str(uuid4()),
                "trace",
                int(time.time() * 1000) + 20000,
                CAPABILITY,
            ),
            Reference("input", hashlib.sha256(self.data).hexdigest(), len(self.data), DOCX),
            "read-grant",
            "write-grant",
        )
        self.revoked = False
        self.on_stage: Callable[[], None] = lambda: None
        self.staged: list[bytes] = []
        self.bad_receipt = False

    def authorize(self, request: Request) -> None:
        if self.revoked or request != self.request:
            raise PermissionError("grant_denied")

    def read(self, request: Request, limit: int, check: Callable[[], None]) -> bytes:
        check()
        self.authorize(request)
        assert len(self.data) <= limit
        return self.data

    def stage(self, request: Request, data: bytes, check: Callable[[], None]) -> Reference:
        check()
        self.authorize(request)
        self.staged.append(data)
        self.on_stage()
        return Reference(
            "output",
            "b" * 64 if self.bad_receipt else hashlib.sha256(data).hexdigest(),
            len(data),
            OUTPUT_MEDIA,
        )


def active() -> None:
    pass


def test_result_binds_execution_and_input() -> None:
    objects = Objects()
    result = DocumentWorker(objects).extract(objects.request, active)
    assert result.execution == objects.request.execution
    assert result.input_sha256 == objects.request.input.sha256
    assert result.output.sha256 == hashlib.sha256(objects.staged[0]).hexdigest()


def test_cross_tenant_denied_before_read() -> None:
    objects = Objects()
    other = replace(objects.request.execution, tenant_id=str(uuid4()))
    with pytest.raises(PermissionError, match="grant_denied"):
        DocumentWorker(objects).extract(replace(objects.request, execution=other), active)
    assert not objects.staged


def test_corrupt_input_has_no_output() -> None:
    objects = Objects()
    objects.data = b"changed"
    with pytest.raises(WorkerError, match="input_digest_mismatch"):
        DocumentWorker(objects).extract(objects.request, active)
    assert not objects.staged


def test_revoked_during_stage_never_returns_result() -> None:
    objects = Objects()
    objects.on_stage = lambda: setattr(objects, "revoked", True)
    with pytest.raises(PermissionError, match="grant_denied"):
        DocumentWorker(objects).extract(objects.request, active)
    assert len(objects.staged) == 1  # Uncommitted object, not a task state transition.


def test_cancelled_after_stage_never_returns_result() -> None:
    objects = Objects()
    cancelled = False

    def cancel() -> None:
        nonlocal cancelled
        cancelled = True

    def check() -> None:
        if cancelled:
            raise TimeoutError("cancelled")

    objects.on_stage = cancel
    with pytest.raises(TimeoutError, match="cancelled"):
        DocumentWorker(objects).extract(objects.request, check)


def test_bad_output_receipt_refused() -> None:
    objects = Objects()
    objects.bad_receipt = True
    with pytest.raises(WorkerError, match="output_receipt_mismatch"):
        DocumentWorker(objects).extract(objects.request, active)


def test_slot_released_after_failure() -> None:
    objects = Objects()
    worker = DocumentWorker(objects, concurrency=1)
    objects.revoked = True
    with pytest.raises(PermissionError):
        worker.extract(objects.request, active)
    objects.revoked = False
    assert worker.extract(objects.request, active).output.id == "output"


@pytest.mark.parametrize("failure", ["none", "revoked", "digest", "backend", "deadline"])
def test_real_grpc_contract(failure: str) -> None:
    objects = Objects()
    request = objects.request
    message = wire.ExtractRequest(
        execution=wire.Execution(**asdict(request.execution)),
        input=wire.ObjectReference(**asdict(request.input)),
        input_grant=request.input_grant,
        output_grant=request.output_grant,
    )
    expected = {
        "revoked": grpc.StatusCode.PERMISSION_DENIED,
        "digest": grpc.StatusCode.DATA_LOSS,
        "backend": grpc.StatusCode.INTERNAL,
        "deadline": grpc.StatusCode.INVALID_ARGUMENT,
    }
    if failure == "revoked":
        objects.revoked = True
    elif failure == "digest":
        objects.data = b"corrupt"
    elif failure == "backend":

        def fail() -> None:
            raise OSError("synthetic-secret-never-return")

        objects.on_stage = fail
    with ThreadPoolExecutor(max_workers=2) as executor:
        server = grpc.server(executor, options=SERVER_OPTIONS)
        register(server, DocumentWorker(objects))
        port = server.add_insecure_port("127.0.0.1:0")
        assert port
        server.start()
        try:
            with grpc.insecure_channel(f"127.0.0.1:{port}") as channel:
                stub = compute_pb2_grpc.DocumentWorkerStub(channel)
                if failure == "none":
                    result = stub.Extract(message, timeout=5)
                    assert result.execution == message.execution
                    assert result.input_sha256 == message.input.sha256
                    assert result.output.sha256 == hashlib.sha256(objects.staged[0]).hexdigest()
                else:
                    with pytest.raises(grpc.RpcError) as caught:
                        stub.Extract(message, timeout=None if failure == "deadline" else 5)
                    assert caught.value.code() == expected[failure]
                    assert "synthetic-secret" not in caught.value.details()
        finally:
            server.stop(0).wait(2)
