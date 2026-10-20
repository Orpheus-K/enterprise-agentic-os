"""Library-only gRPC adapter. Registration does not attest process isolation."""

from dataclasses import asdict
from typing import NoReturn

import grpc

from agentos_compute.documents import DocumentError
from agentos_compute.generated import compute_pb2 as wire
from agentos_compute.worker import DocumentWorker, Execution, Reference, Request, WorkerError

MAX_RPC_BYTES = 32768
SERVER_OPTIONS = (
    ("grpc.max_receive_message_length", MAX_RPC_BYTES),
    ("grpc.max_send_message_length", MAX_RPC_BYTES),
)


def _abort(context: grpc.ServicerContext, code: grpc.StatusCode, reason: str) -> NoReturn:
    context.abort(code, reason)
    raise RuntimeError("RPC abort returned unexpectedly")


class DocumentRPC:
    def __init__(self, worker: DocumentWorker) -> None:
        self._worker = worker

    def extract(
        self, message: wire.ExtractRequest, context: grpc.ServicerContext
    ) -> wire.ExtractResponse:
        remaining = context.time_remaining()
        if remaining is None or remaining > 30:
            _abort(context, grpc.StatusCode.INVALID_ARGUMENT, "bounded_rpc_deadline_required")
        if message.ByteSize() > MAX_RPC_BYTES:
            _abort(context, grpc.StatusCode.RESOURCE_EXHAUSTED, "rpc_size")

        def check() -> None:
            left = context.time_remaining()
            if left is not None and left <= 0:
                raise WorkerError("deadline_exceeded")
            if not context.is_active():
                raise WorkerError("cancelled")

        execution = message.execution
        request = Request(
            Execution(
                execution.tenant_id,
                execution.task_id,
                execution.step_id,
                execution.execution_revision,
                execution.lease_generation,
                execution.request_id,
                execution.trace_id,
                execution.deadline_unix_ms,
                execution.capability_version,
            ),
            Reference(
                message.input.id,
                message.input.sha256,
                message.input.size_bytes,
                message.input.media_type,
            ),
            message.input_grant,
            message.output_grant,
        )
        try:
            result = self._worker.extract(request, check)
        except PermissionError:
            _abort(context, grpc.StatusCode.PERMISSION_DENIED, "object_access_denied")
        except DocumentError as error:
            reason = str(error)
            if reason not in {
                "input_size",
                "output_size",
                "unsupported_media_type",
                "malformed_document",
                "encrypted_document",
                "active_document",
                "page_limit",
                "ocr_required",
                "archive_size",
                "unsafe_archive",
                "unsupported_xml_encoding",
                "unsafe_xml",
                "external_relationship",
            }:
                reason = "document_rejected"
            _abort(context, grpc.StatusCode.INVALID_ARGUMENT, reason)
        except WorkerError as error:
            reason = str(error)
            if reason not in {
                "deadline_exceeded",
                "cancelled",
                "worker_busy",
                "input_digest_mismatch",
                "output_receipt_mismatch",
                "invalid_execution",
                "invalid_object_request",
            }:
                _abort(context, grpc.StatusCode.INTERNAL, "document_worker_failed")
            code = {
                "deadline_exceeded": grpc.StatusCode.DEADLINE_EXCEEDED,
                "cancelled": grpc.StatusCode.CANCELLED,
                "worker_busy": grpc.StatusCode.RESOURCE_EXHAUSTED,
                "input_digest_mismatch": grpc.StatusCode.DATA_LOSS,
                "output_receipt_mismatch": grpc.StatusCode.DATA_LOSS,
            }.get(reason, grpc.StatusCode.INVALID_ARGUMENT)
            _abort(context, code, reason)
        except TimeoutError:
            _abort(context, grpc.StatusCode.DEADLINE_EXCEEDED, "object_access_timeout")
        except Exception:
            # Responsibility boundary: backend exception strings can contain scoped grants.
            # No fallback output and no raw exception content crosses the RPC.
            _abort(context, grpc.StatusCode.INTERNAL, "document_worker_failed")
        return wire.ExtractResponse(
            execution=wire.Execution(**asdict(result.execution)),
            input_sha256=result.input_sha256,
            output=wire.ObjectReference(**asdict(result.output)),
        )


def register(server: grpc.Server, worker: DocumentWorker) -> None:
    adapter = DocumentRPC(worker)
    server.add_generic_rpc_handlers(
        (
            grpc.method_handlers_generic_handler(
                "agentos.compute.v1.DocumentWorker",
                {
                    "Extract": grpc.unary_unary_rpc_method_handler(
                        adapter.extract,
                        request_deserializer=wire.ExtractRequest.FromString,
                        response_serializer=wire.ExtractResponse.SerializeToString,
                    )
                },
            ),
        )
    )
