import hashlib
import io
import threading
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


def test_rpc_concurrency_cancellation_and_oversize() -> None:
    objects = Objects()
    entered = threading.Event()
    release = threading.Event()
    finished = threading.Event()

    def blocked_read(request: Request, limit: int, check: Callable[[], None]) -> bytes:
        entered.set()
        try:
            assert release.wait(5), "test release timed out"
            # Client cancellation is asynchronous; wait for the server context,
            # not a scheduling assumption about when the cancel frame arrives.
            cancellation_deadline = time.monotonic() + 1
            while time.monotonic() < cancellation_deadline:
                check()
                threading.Event().wait(0.01)
            raise AssertionError("server did not observe RPC cancellation")
        finally:
            finished.set()

    objects.read = blocked_read
    request = objects.request
    message = wire.ExtractRequest(
        execution=wire.Execution(**asdict(request.execution)),
        input=wire.ObjectReference(**asdict(request.input)),
        input_grant=request.input_grant,
        output_grant=request.output_grant,
    )
    with ThreadPoolExecutor(max_workers=2) as executor:
        server = grpc.server(executor, options=SERVER_OPTIONS)
        register(server, DocumentWorker(objects, concurrency=1))
        port = server.add_insecure_port("127.0.0.1:0")
        assert port
        server.start()
        try:
            with grpc.insecure_channel(f"127.0.0.1:{port}") as channel:
                stub = compute_pb2_grpc.DocumentWorkerStub(channel)
                pending = stub.Extract.future(message, timeout=5)
                assert entered.wait(3)
                with pytest.raises(grpc.RpcError) as busy:
                    stub.Extract(message, timeout=2)
                assert busy.value.code() == grpc.StatusCode.RESOURCE_EXHAUSTED
                assert busy.value.details() == "worker_busy"
                assert pending.cancel()
                with pytest.raises(grpc.FutureCancelledError):
                    pending.result()
                release.set()
                assert finished.wait(3)
                oversized = wire.ExtractRequest(input_grant="x" * 40000)
                with pytest.raises(grpc.RpcError) as large:
                    stub.Extract(oversized, timeout=2)
                assert large.value.code() == grpc.StatusCode.RESOURCE_EXHAUSTED
        finally:
            release.set()
            server.stop(0).wait(3)
    assert not objects.staged


@pytest.mark.parametrize(
    "field,value",
    [
        ("lease_generation", 0),
        ("execution_revision", "latest"),
        ("capability_version", "document.extract.v2"),
        ("deadline_unix_ms", 1),
        ("tenant_id", "not-uuid"),
    ],
)
def test_invalid_execution_has_no_output(field: str, value: object) -> None:
    objects = Objects()
    execution = replace(objects.request.execution, **{field: value})
    with pytest.raises(WorkerError, match="invalid_execution"):
        DocumentWorker(objects).extract(replace(objects.request, execution=execution), active)
    assert not objects.staged
