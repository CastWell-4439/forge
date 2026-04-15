"""Tests for the Worker class."""

import json
import threading
import time

import grpc
import pytest
from concurrent import futures

from forge_sdk import Worker, task_handler
from forge_sdk.decorators import TaskContext, clear_registry
from forge_sdk.generated import worker_pb2, worker_pb2_grpc


@pytest.fixture(autouse=True)
def _clean_registry():
    """Clear the handler registry before and after each test."""
    clear_registry()
    yield
    clear_registry()


# ---------------------------------------------------------------------------
# Helper: a fake Coordinator gRPC server that accepts Register RPCs
# ---------------------------------------------------------------------------

class _FakeCoordinatorServicer(worker_pb2_grpc.WorkerServiceServicer):
    """Minimal Coordinator stub that accepts Worker.Register calls."""

    def __init__(self):
        self.registrations = []

    def Register(self, request, context):
        self.registrations.append(request.registration)
        return worker_pb2.RegisterResponse(accepted=True, message="ok")


def _start_fake_coordinator():
    """Start a fake Coordinator gRPC server, return (server, port, servicer)."""
    servicer = _FakeCoordinatorServicer()
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=2))
    worker_pb2_grpc.add_WorkerServiceServicer_to_server(servicer, server)
    port = server.add_insecure_port("[::]:0")
    server.start()
    return server, port, servicer


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------

class TestWorkerInstantiation:
    """Tests for Worker construction."""

    def test_default_worker_id(self):
        w = Worker(coordinator="localhost:9999")
        assert w.worker_id  # should be a non-empty string (UUID)
        assert len(w.worker_id) == 36  # UUID format

    def test_custom_worker_id(self):
        w = Worker(coordinator="localhost:9999", worker_id="my-worker")
        assert w.worker_id == "my-worker"

    def test_default_handlers_empty(self):
        w = Worker(coordinator="localhost:9999")
        assert w.handlers == {}

    def test_manual_handler_registration(self):
        w = Worker(coordinator="localhost:9999")
        w.register_handler("test.handler", lambda ctx, params: {"ok": True})
        assert "test.handler" in w.handlers

    def test_no_handlers_raises_on_start(self):
        """Worker.start() should fail if no handlers are registered."""
        w = Worker(coordinator="localhost:9999")
        with pytest.raises(RuntimeError, match="no task handlers"):
            w.start()


class TestWorkerRegistration:
    """Tests for Worker registration with the Coordinator."""

    def test_register_with_coordinator(self):
        server, port, servicer = _start_fake_coordinator()
        try:
            w = Worker(
                coordinator=f"localhost:{port}",
                worker_id="test-worker-reg",
                capacity=3,
                labels={"gpu": "true"},
            )
            w.register_handler("ai.generate", lambda ctx, params: {})
            w.register_handler("ai.summarize", lambda ctx, params: {})

            # Start worker in a background thread, then stop it
            t = threading.Thread(target=w.start, daemon=True)
            t.start()

            # Give it time to register and start
            time.sleep(0.5)
            w.stop()
            t.join(timeout=5)

            assert len(servicer.registrations) == 1
            reg = servicer.registrations[0]
            assert reg.id == "test-worker-reg"
            assert reg.capacity == 3
            assert reg.labels["gpu"] == "true"
            assert set(reg.handlers) == {"ai.generate", "ai.summarize"}
        finally:
            server.stop(grace=0)


class TestTaskExecution:
    """Tests for task execution via gRPC."""

    def _setup_worker(self):
        """Start a fake coordinator and a Worker, return (worker, worker_port, coord_server)."""
        coord_server, coord_port, _ = _start_fake_coordinator()

        w = Worker(
            coordinator=f"localhost:{coord_port}",
            worker_id="exec-worker",
            capacity=5,
        )
        return w, coord_server, coord_port

    def test_execute_task_success(self):
        coord_server, coord_port, _ = _start_fake_coordinator()
        try:
            w = Worker(
                coordinator=f"localhost:{coord_port}",
                worker_id="exec-worker",
                capacity=5,
            )
            w.register_handler(
                "math.double",
                lambda ctx, params: {"result": params["value"] * 2},
            )

            t = threading.Thread(target=w.start, daemon=True)
            t.start()
            time.sleep(0.5)

            # Connect to the Worker's gRPC server and send a task
            channel = grpc.insecure_channel(f"localhost:{w._listen_port}")
            stub = worker_pb2_grpc.WorkerServiceStub(channel)

            response = stub.ExecuteTask(worker_pb2.TaskRequest(
                task_id="task-1",
                workflow_id="wf-1",
                task_name="double",
                handler="math.double",
                input=json.dumps({"value": 21}).encode(),
                timeout_ms=5000,
            ))

            assert response.success is True
            assert response.task_id == "task-1"
            result = json.loads(response.output)
            assert result["result"] == 42

            channel.close()
            w.stop()
            t.join(timeout=5)
        finally:
            coord_server.stop(grace=0)

    def test_execute_task_handler_not_found(self):
        coord_server, coord_port, _ = _start_fake_coordinator()
        try:
            w = Worker(
                coordinator=f"localhost:{coord_port}",
                worker_id="exec-worker-2",
                capacity=5,
            )
            w.register_handler("existing.handler", lambda ctx, params: {})

            t = threading.Thread(target=w.start, daemon=True)
            t.start()
            time.sleep(0.5)

            channel = grpc.insecure_channel(f"localhost:{w._listen_port}")
            stub = worker_pb2_grpc.WorkerServiceStub(channel)

            response = stub.ExecuteTask(worker_pb2.TaskRequest(
                task_id="task-2",
                workflow_id="wf-2",
                task_name="missing",
                handler="nonexistent.handler",
                input=b"{}",
                timeout_ms=5000,
            ))

            assert response.success is False
            assert "not found" in response.error_msg

            channel.close()
            w.stop()
            t.join(timeout=5)
        finally:
            coord_server.stop(grace=0)

    def test_execute_task_handler_exception(self):
        coord_server, coord_port, _ = _start_fake_coordinator()
        try:
            def failing_handler(ctx, params):
                raise RuntimeError("something went wrong")

            w = Worker(
                coordinator=f"localhost:{coord_port}",
                worker_id="exec-worker-3",
                capacity=5,
            )
            w.register_handler("fail.handler", failing_handler)

            t = threading.Thread(target=w.start, daemon=True)
            t.start()
            time.sleep(0.5)

            channel = grpc.insecure_channel(f"localhost:{w._listen_port}")
            stub = worker_pb2_grpc.WorkerServiceStub(channel)

            response = stub.ExecuteTask(worker_pb2.TaskRequest(
                task_id="task-3",
                workflow_id="wf-3",
                task_name="failing",
                handler="fail.handler",
                input=b"{}",
                timeout_ms=5000,
            ))

            assert response.success is False
            assert "something went wrong" in response.error_msg

            channel.close()
            w.stop()
            t.join(timeout=5)
        finally:
            coord_server.stop(grace=0)

    def test_execute_task_with_decorator(self):
        """Test that @task_handler-decorated functions are picked up by Worker."""
        coord_server, coord_port, _ = _start_fake_coordinator()
        try:
            @task_handler("decorated.handler")
            def my_decorated(ctx, params):
                return {"from_decorator": True, "name": params.get("name", "")}

            w = Worker(
                coordinator=f"localhost:{coord_port}",
                worker_id="exec-worker-4",
                capacity=5,
            )

            t = threading.Thread(target=w.start, daemon=True)
            t.start()
            time.sleep(0.5)

            assert "decorated.handler" in w.handlers

            channel = grpc.insecure_channel(f"localhost:{w._listen_port}")
            stub = worker_pb2_grpc.WorkerServiceStub(channel)

            response = stub.ExecuteTask(worker_pb2.TaskRequest(
                task_id="task-4",
                workflow_id="wf-4",
                task_name="decorated",
                handler="decorated.handler",
                input=json.dumps({"name": "forge"}).encode(),
                timeout_ms=5000,
            ))

            assert response.success is True
            result = json.loads(response.output)
            assert result["from_decorator"] is True
            assert result["name"] == "forge"

            channel.close()
            w.stop()
            t.join(timeout=5)
        finally:
            coord_server.stop(grace=0)

    def test_execute_task_invalid_json_input(self):
        coord_server, coord_port, _ = _start_fake_coordinator()
        try:
            w = Worker(
                coordinator=f"localhost:{coord_port}",
                worker_id="exec-worker-5",
                capacity=5,
            )
            w.register_handler("any.handler", lambda ctx, params: {})

            t = threading.Thread(target=w.start, daemon=True)
            t.start()
            time.sleep(0.5)

            channel = grpc.insecure_channel(f"localhost:{w._listen_port}")
            stub = worker_pb2_grpc.WorkerServiceStub(channel)

            response = stub.ExecuteTask(worker_pb2.TaskRequest(
                task_id="task-5",
                workflow_id="wf-5",
                task_name="bad-input",
                handler="any.handler",
                input=b"not-valid-json{{{",
                timeout_ms=5000,
            ))

            assert response.success is False
            assert "decode input JSON" in response.error_msg

            channel.close()
            w.stop()
            t.join(timeout=5)
        finally:
            coord_server.stop(grace=0)


class TestHeartbeat:
    """Tests for the Heartbeat RPC."""

    def test_heartbeat_responds_to_ping(self):
        coord_server, coord_port, _ = _start_fake_coordinator()
        try:
            w = Worker(
                coordinator=f"localhost:{coord_port}",
                worker_id="hb-worker",
                capacity=10,
            )
            w.register_handler("dummy", lambda ctx, params: {})

            t = threading.Thread(target=w.start, daemon=True)
            t.start()
            time.sleep(0.5)

            # Connect to Worker and open a Heartbeat stream
            channel = grpc.insecure_channel(f"localhost:{w._listen_port}")
            stub = worker_pb2_grpc.WorkerServiceStub(channel)

            from google.protobuf import timestamp_pb2

            def ping_generator():
                for _ in range(3):
                    now = timestamp_pb2.Timestamp()
                    now.GetCurrentTime()
                    yield worker_pb2.HeartbeatPing(timestamp=now)
                    time.sleep(0.1)

            responses = list(stub.Heartbeat(ping_generator()))
            assert len(responses) == 3
            for pong in responses:
                assert pong.worker_id == "hb-worker"
                assert pong.capacity == 10
                assert pong.active_tasks >= 0

            channel.close()
            w.stop()
            t.join(timeout=5)
        finally:
            coord_server.stop(grace=0)
