"""
Forge Trace Context Propagation (W3C Traceparent).

Enables distributed tracing across Go Coordinator → Python Worker.
Extracts/injects trace context from/to gRPC metadata.
"""

import os
import random
import time
from dataclasses import dataclass, field
from typing import Dict, Optional, Callable


@dataclass
class SpanContext:
    """W3C Trace Context."""
    trace_id: str  # 32 hex chars
    span_id: str   # 16 hex chars
    sampled: bool = True

    def traceparent(self) -> str:
        flag = "01" if self.sampled else "00"
        return f"00-{self.trace_id}-{self.span_id}-{flag}"

    @staticmethod
    def from_traceparent(tp: str) -> Optional["SpanContext"]:
        parts = tp.split("-")
        if len(parts) != 4 or parts[0] != "00":
            return None
        return SpanContext(
            trace_id=parts[1],
            span_id=parts[2],
            sampled=parts[3] == "01",
        )


@dataclass
class Span:
    """A single span in a trace."""
    name: str
    context: SpanContext
    parent_id: str = ""
    start_time: float = field(default_factory=time.time)
    end_time: float = 0.0
    attributes: Dict[str, str] = field(default_factory=dict)
    status: str = "UNSET"  # UNSET, OK, ERROR

    def set_attribute(self, key: str, value: str):
        self.attributes[key] = value

    def set_status(self, status: str, message: str = ""):
        self.status = status
        if message:
            self.attributes["status.message"] = message

    def end(self):
        self.end_time = time.time()

    @property
    def duration_ms(self) -> float:
        if self.end_time <= 0:
            return 0
        return (self.end_time - self.start_time) * 1000


def _random_hex(length: int) -> str:
    return "".join(f"{random.randint(0, 255):02x}" for _ in range(length))


class Tracer:
    """Simple tracer for Python Worker SDK."""

    def __init__(self, service_name: str = "forge-worker-python", sample_rate: float = 1.0):
        self.service_name = service_name
        self.sample_rate = sample_rate
        self._spans: list[Span] = []

    def start_span(self, name: str, parent: Optional[SpanContext] = None) -> Span:
        if parent:
            trace_id = parent.trace_id
            parent_id = parent.span_id
            sampled = parent.sampled
        else:
            trace_id = _random_hex(16)
            parent_id = ""
            sampled = random.random() < self.sample_rate

        span = Span(
            name=name,
            context=SpanContext(
                trace_id=trace_id,
                span_id=_random_hex(8),
                sampled=sampled,
            ),
            parent_id=parent_id,
        )
        span.set_attribute("service.name", self.service_name)
        self._spans.append(span)
        return span

    def end_span(self, span: Span):
        span.end()

    @property
    def exported_spans(self) -> list[Span]:
        return [s for s in self._spans if s.end_time > 0]


def extract_from_grpc_metadata(metadata: Dict[str, str]) -> Optional[SpanContext]:
    """Extract trace context from gRPC metadata (incoming request)."""
    tp = metadata.get("traceparent") or metadata.get("Traceparent")
    if not tp:
        return None
    return SpanContext.from_traceparent(tp)


def inject_to_grpc_metadata(span: Span) -> Dict[str, str]:
    """Inject trace context into gRPC metadata (outgoing request)."""
    return {"traceparent": span.context.traceparent()}
