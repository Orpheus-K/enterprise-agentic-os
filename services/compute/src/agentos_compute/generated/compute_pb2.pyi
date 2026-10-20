from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class Execution(_message.Message):
    __slots__ = ("tenant_id", "task_id", "step_id", "execution_revision", "lease_generation", "request_id", "trace_id", "deadline_unix_ms", "capability_version")
    TENANT_ID_FIELD_NUMBER: _ClassVar[int]
    TASK_ID_FIELD_NUMBER: _ClassVar[int]
    STEP_ID_FIELD_NUMBER: _ClassVar[int]
    EXECUTION_REVISION_FIELD_NUMBER: _ClassVar[int]
    LEASE_GENERATION_FIELD_NUMBER: _ClassVar[int]
    REQUEST_ID_FIELD_NUMBER: _ClassVar[int]
    TRACE_ID_FIELD_NUMBER: _ClassVar[int]
    DEADLINE_UNIX_MS_FIELD_NUMBER: _ClassVar[int]
    CAPABILITY_VERSION_FIELD_NUMBER: _ClassVar[int]
    tenant_id: str
    task_id: str
    step_id: str
    execution_revision: str
    lease_generation: int
    request_id: str
    trace_id: str
    deadline_unix_ms: int
    capability_version: str
    def __init__(self, tenant_id: _Optional[str] = ..., task_id: _Optional[str] = ..., step_id: _Optional[str] = ..., execution_revision: _Optional[str] = ..., lease_generation: _Optional[int] = ..., request_id: _Optional[str] = ..., trace_id: _Optional[str] = ..., deadline_unix_ms: _Optional[int] = ..., capability_version: _Optional[str] = ...) -> None: ...

class ObjectReference(_message.Message):
    __slots__ = ("id", "sha256", "size_bytes", "media_type")
    ID_FIELD_NUMBER: _ClassVar[int]
    SHA256_FIELD_NUMBER: _ClassVar[int]
    SIZE_BYTES_FIELD_NUMBER: _ClassVar[int]
    MEDIA_TYPE_FIELD_NUMBER: _ClassVar[int]
    id: str
    sha256: str
    size_bytes: int
    media_type: str
    def __init__(self, id: _Optional[str] = ..., sha256: _Optional[str] = ..., size_bytes: _Optional[int] = ..., media_type: _Optional[str] = ...) -> None: ...

class ExtractRequest(_message.Message):
    __slots__ = ("execution", "input", "input_grant", "output_grant")
    EXECUTION_FIELD_NUMBER: _ClassVar[int]
    INPUT_FIELD_NUMBER: _ClassVar[int]
    INPUT_GRANT_FIELD_NUMBER: _ClassVar[int]
    OUTPUT_GRANT_FIELD_NUMBER: _ClassVar[int]
    execution: Execution
    input: ObjectReference
    input_grant: str
    output_grant: str
    def __init__(self, execution: _Optional[_Union[Execution, _Mapping]] = ..., input: _Optional[_Union[ObjectReference, _Mapping]] = ..., input_grant: _Optional[str] = ..., output_grant: _Optional[str] = ...) -> None: ...

class ExtractResponse(_message.Message):
    __slots__ = ("execution", "input_sha256", "output", "block_count")
    EXECUTION_FIELD_NUMBER: _ClassVar[int]
    INPUT_SHA256_FIELD_NUMBER: _ClassVar[int]
    OUTPUT_FIELD_NUMBER: _ClassVar[int]
    BLOCK_COUNT_FIELD_NUMBER: _ClassVar[int]
    execution: Execution
    input_sha256: str
    output: ObjectReference
    block_count: int
    def __init__(self, execution: _Optional[_Union[Execution, _Mapping]] = ..., input_sha256: _Optional[str] = ..., output: _Optional[_Union[ObjectReference, _Mapping]] = ..., block_count: _Optional[int] = ...) -> None: ...
