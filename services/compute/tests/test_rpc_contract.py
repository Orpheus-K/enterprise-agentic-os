"""Verify portable bindings against the source contract and a real loopback RPC."""

import subprocess
import sys
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

import grpc

from agentos_compute.generated import compute_pb2, compute_pb2_grpc


def test_generated_bindings_match_source(tmp_path: Path) -> None:
    compute = Path(__file__).resolve().parents[1]
    contracts = compute.parents[1] / "contracts"
    subprocess.run(
        [
            sys.executable,
            "-m",
            "grpc_tools.protoc",
            f"-Iagentos_compute/generated={contracts}",
            f"--python_out={tmp_path}",
            f"--pyi_out={tmp_path}",
            f"--grpc_python_out={tmp_path}",
            str(contracts / "compute.proto"),
        ],
        check=True,
    )
    for name in ("compute_pb2.py", "compute_pb2.pyi", "compute_pb2_grpc.py"):
        actual = compute / "src" / "agentos_compute" / "generated" / name
        expected = tmp_path / "agentos_compute" / "generated" / name
        assert actual.read_bytes() == expected.read_bytes()


def test_unimplemented_rpc_fails_explicitly() -> None:
    with ThreadPoolExecutor(max_workers=1) as executor:
        server = grpc.server(executor)
        compute_pb2_grpc.add_DocumentWorkerServicer_to_server(
            compute_pb2_grpc.DocumentWorkerServicer(), server
        )
        port = server.add_insecure_port("127.0.0.1:0")
        assert port != 0
        server.start()
        try:
            with grpc.insecure_channel(f"127.0.0.1:{port}") as channel:
                stub = compute_pb2_grpc.DocumentWorkerStub(channel)
                try:
                    stub.Extract(compute_pb2.ExtractRequest(), timeout=2)
                except grpc.RpcError as error:
                    assert error.code() == grpc.StatusCode.UNIMPLEMENTED
                else:
                    raise AssertionError("unimplemented document extraction returned success")
        finally:
            server.stop(0).wait(2)
