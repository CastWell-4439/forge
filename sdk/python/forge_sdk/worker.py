"""Forge Python Worker — connects to Coordinator, executes tasks."""

import json
import logging
import signal
import socket
import threading
import uuid
from concurrent import futures
from typing import Callable, Dict, Optional

import grpc
from google.protobuf import timestamp_pb2

from .decorators import TaskContext, get_registered_handlers
from .generated import worker_pb2, worker_pb2_grpc

logger = logging.getLogger("forge.worker")


class _WorkerServicer(worker_pb2_grpc.WorkerServiceServicer):
    """gRPC servicer implementing WorkerService.

    Handles ExecuteTask and Heartbeat RPCs initiated by the Coordinator.
    """

    def __init__(self, worker: "Worker") -> None:
        self._worker = worker

    def Register(self, request, context):
        """No-op — the Worker registers itself with the Coordinator, not the other way around."""
        return worker_pb2.RegisterResponse(accepted=True)

    def Heartbeat(self, request_iterator, context):
        """Bidirectional heartbeat stream.

        The Coordinator sends HeartbeatPing messages; we respond with HeartbeatPong
        containing current active task count and capacity.
        """
        for ping in request_iterator:
            with self._worker._active_lock:
                active = self._worker._active_tasks

            now = timestamp_pb2.Timestamp()
            now.GetCurrentTime()
            yield worker_pb2.HeartbeatPong(
                worker_id=self._worker._worker_id,
                active_tasks=active,
                capacity=self._worker._capacity,
                timestamp=now,
            )

    def ExecuteTask(self, request: worker_pb2.TaskRequest, context) -> worker_pb2.TaskResponse:
        """Dispatch an incoming task to the appropriate handler."""
        handler_name = request.handler
        handler_fn = self._worker._handlers.get(handler_name)

        if handler_fn is None:
            return worker_pb2.TaskResponse(
                task_id=request.task_id,
                success=False,
                error_msg=f"handler '{handler_name}' not found",
            )

        # Build TaskContext
        ctx = TaskContext(
            task_id=request.task_id,
            workflow_id=request.workflow_id,
            task_name=request.task_name,
            handler=handler_name,
            timeout_ms=request.timeout_ms,
            worker_id=self._worker._worker_id,
        )

        # Decode JSON input
        try:
            params = json.loads(request.input) if request.input else {}
        except (json.JSONDecodeError, UnicodeDecodeError) as exc:
            return worker_pb2.TaskResponse(
                task_id=request.task_id,
                success=False,
                error_msg=f"failed to decode input JSON: {exc}",
            )

        # Track active tasks
        with self._worker._active_lock:
            self._worker._active_tasks += 1

        try:
            result = handler_fn(ctx, params)
            output_bytes = json.dumps(result).encode("utf-8") if result is not None else b"{}"
            return worker_pb2.TaskResponse(
                task_id=request.task_id,
                success=True,
                output=output_bytes,
            )
        except Exception as exc:
            logger.error("handler '%s' failed for task %s: %s", handler_name, request.task_id, exc)
            return worker_pb2.TaskResponse(
                task_id=request.task_id,
                success=False,
                error_msg=str(exc),
            )
        finally:
            with self._worker._active_lock:
                self._worker._active_tasks -= 1


class Worker:
    """Forge Python Worker.

    Connects to a Coordinator via gRPC, auto-registers with labels/handlers/capacity,
    serves a gRPC endpoint for Heartbeat and ExecuteTask RPCs from the Coordinator,
    and supports graceful shutdown.

    Args:
        coordinator: Coordinator gRPC address (e.g., ``"localhost:8080"``).
        worker_id: Unique worker identifier. Auto-generated UUID if omitted.
        capacity: Maximum concurrent tasks this Worker can handle.
        labels: Key-value scheduling hints (e.g., ``{"gpu": "true"}``).
        listen_port: Port for the Worker's gRPC server (0 = auto-pick).
    """

    def __init__(
        self,
        coordinator: str,
        worker_id: Optional[str] = None,
        capacity: int = 5,
        labels: Optional[Dict[str, str]] = None,
        listen_port: int = 0,
    ) -> None:
        self._coordinator_addr = coordinator
        self._worker_id = worker_id or str(uuid.uuid4())
        self._capacity = capacity
        self._labels = labels or {}
        self._listen_port = listen_port

        # Handler dispatch table: populated from decorator registry + manual registration
        self._handlers: Dict[str, Callable] = {}

        # Active task tracking
        self._active_tasks = 0
        self._active_lock = threading.Lock()

        # Shutdown coordination
        self._stop_event = threading.Event()
        self._server: Optional[grpc.Server] = None
        self._channel: Optional[grpc.Channel] = None

    @property
    def worker_id(self) -> str:
        """Return this Worker's unique identifier."""
        return self._worker_id

    @property
    def handlers(self) -> Dict[str, Callable]:
        """Return the current handler dispatch table."""
        return dict(self._handlers)

    def register_handler(self, name: str, func: Callable) -> None:
        """Manually register a task handler.

        Args:
            name: Handler identifier (e.g., ``"ai.generate"``).
            func: Callable accepting ``(ctx: TaskContext, params: dict) -> dict``.
        """
        self._handlers[name] = func

    # ------------------------------------------------------------------
    # Lifecycle
    # ------------------------------------------------------------------

    def start(self) -> None:
        """Start the Worker (blocking).

        1. Collects handlers from the decorator registry.
        2. Starts a gRPC server for receiving ExecuteTask and Heartbeat RPCs.
        3. Registers with the Coordinator.
        4. Blocks until ``stop()`` is called or a signal is received.
        """
        # Merge decorator-registered handlers
        for name, fn in get_registered_handlers().items():
            if name not in self._handlers:
                self._handlers[name] = fn

        if not self._handlers:
            raise RuntimeError("no task handlers registered — use @task_handler or register_handler()")

        # Install signal handlers for graceful shutdown (only works in main thread)
        import threading as _threading
        if _threading.current_thread() is _threading.main_thread():
            signal.signal(signal.SIGINT, self._signal_handler)
            signal.signal(signal.SIGTERM, self._signal_handler)

        # Start gRPC server
        self._server = grpc.server(futures.ThreadPoolExecutor(max_workers=self._capacity))
        servicer = _WorkerServicer(self)
        worker_pb2_grpc.add_WorkerServiceServicer_to_server(servicer, self._server)

        if self._listen_port == 0:
            self._listen_port = self._server.add_insecure_port("[::]:0")
        else:
            self._server.add_insecure_port(f"[::]:{self._listen_port}")

        self._server.start()
        logger.info(
            "worker %s listening on port %d (capacity=%d, handlers=%s)",
            self._worker_id,
            self._listen_port,
            self._capacity,
            list(self._handlers.keys()),
        )

        # Register with Coordinator
        self._register()

        # Block until stop
        self._stop_event.wait()
        self._shutdown()

    def stop(self) -> None:
        """Signal the Worker to shut down gracefully."""
        self._stop_event.set()

    # ------------------------------------------------------------------
    # Internal helpers
    # ------------------------------------------------------------------

    def _signal_handler(self, signum, frame) -> None:
        logger.info("received signal %d, shutting down", signum)
        self.stop()

    def _get_worker_addr(self) -> str:
        """Return the advertised gRPC address of this Worker."""
        hostname = socket.gethostname()
        return f"{hostname}:{self._listen_port}"

    def _register(self) -> None:
        """Register this Worker with the Coordinator via gRPC."""
        self._channel = grpc.insecure_channel(self._coordinator_addr)
        stub = worker_pb2_grpc.WorkerServiceStub(self._channel)

        registration = worker_pb2.WorkerRegistration(
            id=self._worker_id,
            addr=self._get_worker_addr(),
            labels=self._labels,
            capacity=self._capacity,
            handlers=list(self._handlers.keys()),
        )
        request = worker_pb2.RegisterRequest(registration=registration)

        try:
            response = stub.Register(request)
            if response.accepted:
                logger.info("registered with coordinator: %s", response.message)
            else:
                raise RuntimeError(f"coordinator rejected registration: {response.message}")
        except grpc.RpcError as exc:
            raise ConnectionError(
                f"failed to register with coordinator at {self._coordinator_addr}: {exc}"
            ) from exc

    def _shutdown(self) -> None:
        """Perform graceful shutdown."""
        logger.info("shutting down worker %s", self._worker_id)

        # Stop gRPC server with grace period for in-flight tasks
        if self._server is not None:
            self._server.stop(grace=30)

        # Close channel to coordinator
        if self._channel is not None:
            self._channel.close()

        logger.info("worker %s stopped", self._worker_id)
