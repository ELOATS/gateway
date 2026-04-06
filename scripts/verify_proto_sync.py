from __future__ import annotations

import base64
import subprocess
import sys
import tempfile
from pathlib import Path

from google.protobuf import descriptor_pb2
from google.protobuf import descriptor_pool

ROOT = Path(__file__).resolve().parent.parent
PROTO_FILE = ROOT / "proto" / "gateway.proto"
LOGIC_PYTHON_DIR = ROOT / "logic-python"
CORE_GO_DIR = ROOT / "core-go"


def compile_proto_descriptor() -> bytes:
    with tempfile.TemporaryDirectory() as tmpdir:
        descriptor_path = Path(tmpdir) / "gateway.pb"
        subprocess.run(
            [
                "protoc",
                f"--proto_path={ROOT / 'proto'}",
                f"--descriptor_set_out={descriptor_path}",
                "--include_imports",
                str(PROTO_FILE),
            ],
            check=True,
            cwd=ROOT,
        )

        fds = descriptor_pb2.FileDescriptorSet()
        fds.ParseFromString(descriptor_path.read_bytes())
        for file_proto in fds.file:
            if file_proto.name == "gateway.proto":
                normalize_file_descriptor(file_proto)
                return file_proto.SerializeToString(deterministic=True)
        raise RuntimeError("gateway.proto descriptor was not found in descriptor set")


def python_generated_descriptor() -> bytes:
    sys.path.insert(0, str(LOGIC_PYTHON_DIR))
    import gateway_pb2  # type: ignore

    pool = descriptor_pool.Default()
    file_descriptor = pool.FindFileByName("gateway.proto")
    file_proto = descriptor_pb2.FileDescriptorProto()
    file_proto.ParseFromString(file_descriptor.serialized_pb)
    normalize_file_descriptor(file_proto)
    return file_proto.SerializeToString(deterministic=True)


def go_generated_descriptor() -> bytes:
    output = subprocess.run(
        ["go", "run", "./cmd/proto_descriptor"],
        check=True,
        cwd=CORE_GO_DIR,
        capture_output=True,
        text=True,
    ).stdout.strip()
    file_proto = descriptor_pb2.FileDescriptorProto()
    file_proto.ParseFromString(base64.b64decode(output))
    normalize_file_descriptor(file_proto)
    return file_proto.SerializeToString(deterministic=True)


def normalize_file_descriptor(file_proto: descriptor_pb2.FileDescriptorProto) -> None:
    file_proto.ClearField("source_code_info")
    for message in file_proto.message_type:
        normalize_message_descriptor(message)


def normalize_message_descriptor(message: descriptor_pb2.DescriptorProto) -> None:
    for field in message.field:
        field.ClearField("json_name")
    for nested in message.nested_type:
        normalize_message_descriptor(nested)


def main() -> int:
    compiled = compile_proto_descriptor()
    generated_python = python_generated_descriptor()
    generated_go = go_generated_descriptor()

    mismatches: list[str] = []
    if compiled != generated_python:
        mismatches.append("Python generated descriptor does not match proto/gateway.proto")
    if compiled != generated_go:
        mismatches.append("Go generated descriptor does not match proto/gateway.proto")

    if mismatches:
        for mismatch in mismatches:
            print(mismatch)
        return 1

    print("Proto descriptors are in sync for Go and Python generated artifacts.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())