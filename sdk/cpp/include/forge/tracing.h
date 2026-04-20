#pragma once

// Forge Trace Context Propagation (W3C Traceparent)
// Enables distributed tracing across Go Coordinator → C++ Worker.

#include <string>
#include <map>
#include <cstdint>
#include <random>
#include <sstream>
#include <iomanip>
#include <chrono>

namespace forge {

struct SpanContext {
    std::string trace_id;  // 32 hex chars
    std::string span_id;   // 16 hex chars
    bool sampled = true;

    std::string traceparent() const {
        return "00-" + trace_id + "-" + span_id + "-" + (sampled ? "01" : "00");
    }

    static bool from_traceparent(const std::string& tp, SpanContext& out) {
        // Format: 00-{32}-{16}-{2}
        if (tp.size() < 55 || tp[0] != '0' || tp[1] != '0' || tp[2] != '-') {
            return false;
        }
        out.trace_id = tp.substr(3, 32);
        out.span_id = tp.substr(36, 16);
        out.sampled = (tp.substr(53, 2) == "01");
        return true;
    }
};

struct Span {
    std::string name;
    SpanContext context;
    std::string parent_id;
    std::chrono::steady_clock::time_point start_time;
    std::chrono::steady_clock::time_point end_time;
    std::map<std::string, std::string> attributes;
    std::string status = "UNSET";  // UNSET, OK, ERROR

    void set_attribute(const std::string& key, const std::string& value) {
        attributes[key] = value;
    }

    void set_status(const std::string& s, const std::string& message = "") {
        status = s;
        if (!message.empty()) {
            attributes["status.message"] = message;
        }
    }

    void end() {
        end_time = std::chrono::steady_clock::now();
    }

    double duration_ms() const {
        auto dur = std::chrono::duration_cast<std::chrono::microseconds>(end_time - start_time);
        return dur.count() / 1000.0;
    }
};

class Tracer {
public:
    explicit Tracer(const std::string& service_name = "forge-worker-cpp",
                    double sample_rate = 1.0)
        : service_name_(service_name), sample_rate_(sample_rate),
          rng_(std::random_device{}()) {}

    Span start_span(const std::string& name, const SpanContext* parent = nullptr) {
        Span span;
        span.name = name;
        span.start_time = std::chrono::steady_clock::now();

        if (parent) {
            span.context.trace_id = parent->trace_id;
            span.parent_id = parent->span_id;
            span.context.sampled = parent->sampled;
        } else {
            span.context.trace_id = random_hex(16);
            span.context.sampled = (uniform_dist_(rng_) < sample_rate_);
        }
        span.context.span_id = random_hex(8);
        span.set_attribute("service.name", service_name_);
        return span;
    }

    void end_span(Span& span) {
        span.end();
    }

private:
    std::string random_hex(int bytes) {
        std::ostringstream oss;
        for (int i = 0; i < bytes; ++i) {
            oss << std::hex << std::setfill('0') << std::setw(2)
                << (rng_() % 256);
        }
        return oss.str();
    }

    std::string service_name_;
    double sample_rate_;
    std::mt19937 rng_;
    std::uniform_real_distribution<double> uniform_dist_{0.0, 1.0};
};

// Extract trace context from gRPC metadata.
inline bool extract_from_grpc_metadata(
    const std::map<std::string, std::string>& metadata,
    SpanContext& out) {
    auto it = metadata.find("traceparent");
    if (it == metadata.end()) return false;
    return SpanContext::from_traceparent(it->second, out);
}

// Inject trace context into gRPC metadata.
inline std::map<std::string, std::string> inject_to_grpc_metadata(const Span& span) {
    return {{"traceparent", span.context.traceparent()}};
}

} // namespace forge
