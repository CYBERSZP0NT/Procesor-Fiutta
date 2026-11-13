#!/usr/bin/env python3
"""
Plugin: python-example
======================

A reference Python plugin implementing the szmelc-router JSON-RPC 2.0
stdio protocol. Demonstrates:
  – initialize handshake
  – models catalogue
  – availability check
  – streaming response (text_delta notifications + done result)

Wire into szmelc-router config:
  providers:
    - name: python-example
      impl: plugin
      kind: plugin
      command: ["python3", "plugins/python-example/plugin.py"]

  routes:
    py_test:
      - python-example/hello

Build nothing — just run.

Protocol: see docs/plugin-protocol.md
"""
import json
import sys


def write(obj: dict) -> None:
    """Write one JSON-RPC message as a single line to stdout."""
    print(json.dumps(obj), flush=True)


def read() -> dict | None:
    """Read one JSON-RPC message from stdin."""
    line = sys.stdin.readline()
    if not line:
        return None
    line = line.strip()
    if not line:
        return None
    try:
        return json.loads(line)
    except json.JSONDecodeError:
        return None


def handle_initialize(params: dict) -> dict:
    return {"name": "python-example", "version": "1.0.0"}


def handle_models(params: dict) -> list:
    return [
        {"id": "hello", "name": "Hello (basic greeting)"},
        {"id": "repeat_n", "name": "Repeat (echo N times)"},
    ]


def handle_available(params: dict) -> dict:
    return {"ok": True, "reason": "ready"}


def handle_stream(params: dict) -> tuple[dict, list[dict]]:
    """
    Returns (result, notifications).
    notifications are sent before the final result.
    """
    model = params.get("model", "hello")
    messages = params.get("messages", [])
    user_msg = ""
    if messages:
        user_msg = messages[-1].get("content", "")

    notifications = []

    if model == "hello":
        text = f"Hello! You said: {user_msg}"
        # Stream word by word.
        for i, word in enumerate(text.split()):
            if i > 0:
                notifications.append(
                    {
                        "jsonrpc": "2.0",
                        "method": "notification/streams/text_delta",
                        "params": {"text": " "},
                    }
                )
            notifications.append(
                {
                    "jsonrpc": "2.0",
                    "method": "notification/streams/text_delta",
                    "params": {"text": word},
                }
                )
        result = {"finish_reason": "stop", "prompt_tokens": -1, "completion_tokens": -1}

    elif model == "repeat_n":
        # Repeat the user message 3 times.
        for i in range(3):
            notifications.append(
                {
                    "jsonrpc": "2.0",
                    "method": "notification/streams/text_delta",
                    "params": {"text": f"[{i+1}] {user_msg}\n"},
                }
                )
        result = {"finish_reason": "stop", "prompt_tokens": -1, "completion_tokens": -1}

    else:
        raise RuntimeError(f"unknown model {model!r} (available: hello, repeat_n)")

    return result, notifications


def main() -> None:
    while True:
        msg = read()
        if msg is None:
            break  # EOF

        method = msg.get("method")
        msg_id = msg.get("id")
        params = msg.get("params") or {}

        try:
            if method == "initialize":
                result = handle_initialize(params)
                write({"jsonrpc": "2.0", "id": msg_id, "result": result})

            elif method == "models":
                result = handle_models(params)
                write({"jsonrpc": "2.0", "id": msg_id, "result": result})

            elif method == "available":
                result = handle_available(params)
                write({"jsonrpc": "2.0", "id": msg_id, "result": result})

            elif method == "stream":
                result, notifs = handle_stream(params)
                # Send notifications first.
                for n in notifs:
                    write(n)
                # Then the final result.
                write({"jsonrpc": "2.0", "id": msg_id, "result": result})

            elif method == "shutdown":
                if msg_id is not None:
                    write({"jsonrpc": "2.0", "id": msg_id, "result": None})
                break

            else:
                if msg_id is not None:
                    write({
                        "jsonrpc": "2.0",
                        "id": msg_id,
                        "error": {"code": -32601, "message": f"method not found: {method}"},
                    })

        except Exception as e:
            if msg_id is not None:
                write({
                    "jsonrpc": "2.0",
                    "id": msg_id,
                    "error": {"code": -32000, "message": str(e)},
                })


if __name__ == "__main__":
    main()
