"""Example Python Worker — AI task handlers using the Forge SDK.

This example demonstrates how to build a Python Worker that handles
AI-related tasks. The handlers return mock responses (no real API calls).

Usage:
    # Start the Forge Coordinator first, then:
    pip install -e ../../sdk/python
    python main.py
"""

import logging
import os
import time

from forge_sdk import Worker, task_handler

# Configure logging
logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(levelname)s %(name)s: %(message)s",
)


@task_handler("ai.generate")
def handle_ai_generate(ctx, params):
    """AI text generation task — returns a mock response.

    Expected params:
        prompt (str): The input prompt.
        model (str): Model name (optional, default "mock-llm").
    """
    prompt = params.get("prompt", "")
    model = params.get("model", "mock-llm")

    ctx.log("info", "Generating AI response", model=model, prompt_length=len(prompt))
    ctx.set_progress(50, "Processing prompt")

    # Simulate processing time
    time.sleep(0.1)

    ctx.set_progress(100, "Complete")
    return {
        "model": model,
        "output": f"Mock AI response to: {prompt[:100]}",
        "tokens_used": len(prompt.split()),
    }


@task_handler("ai.summarize")
def handle_ai_summarize(ctx, params):
    """AI text summarization task — returns a mock summary.

    Expected params:
        text (str): The text to summarize.
        max_length (int): Maximum summary length (optional, default 100).
    """
    text = params.get("text", "")
    max_length = params.get("max_length", 100)

    ctx.log("info", "Summarizing text", text_length=len(text), max_length=max_length)

    # Mock summarization: take first N characters
    summary = text[:max_length] + ("..." if len(text) > max_length else "")
    return {
        "summary": summary,
        "original_length": len(text),
        "summary_length": len(summary),
    }


@task_handler("ai.classify")
def handle_ai_classify(ctx, params):
    """AI text classification task — returns a mock classification.

    Expected params:
        text (str): The text to classify.
        categories (list[str]): Available categories.
    """
    text = params.get("text", "")
    categories = params.get("categories", ["positive", "negative", "neutral"])

    ctx.log("info", "Classifying text", text_length=len(text), num_categories=len(categories))

    # Mock classification: pick the first category
    return {
        "category": categories[0] if categories else "unknown",
        "confidence": 0.95,
    }


def main():
    coordinator_addr = os.environ.get("FORGE_COORDINATOR", "localhost:8080")

    worker = Worker(
        coordinator=coordinator_addr,
        capacity=5,
        labels={"type": "ai", "language": "python"},
    )

    logging.getLogger("forge.worker").info(
        "Starting Python AI worker (coordinator=%s)", coordinator_addr
    )
    worker.start()


if __name__ == "__main__":
    main()
