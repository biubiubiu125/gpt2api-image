from __future__ import annotations

import base64
import json
import math
import random
import re
import time
from dataclasses import dataclass, field
from typing import Any, Callable


def _to_text(value: Any) -> str:
    if value is None:
        return ""
    return str(value)


def _b64_encode_text(value: Any) -> str:
    return base64.b64encode(_to_text(value).encode("utf-8")).decode("ascii")


def _b64_decode_text(value: Any) -> str:
    raw = str(value or "")
    if not raw:
        return ""
    padding = (-len(raw)) % 4
    if padding:
        raw += "=" * padding
    return base64.b64decode(raw).decode("utf-8", "ignore")


def xor_string(text: str, key: str) -> str:
    if not key:
        return text
    return "".join(chr(ord(ch) ^ ord(key[index % len(key)])) for index, ch in enumerate(text))


class OrderedMap:
    def __init__(self) -> None:
        self.keys: list[str] = []
        self.values: dict[str, Any] = {}

    def add(self, key: Any, value: Any) -> None:
        normalized = _to_text(key)
        if normalized not in self.values:
            self.keys.append(normalized)
        self.values[normalized] = value

    def get(self, key: Any, default: Any = None) -> Any:
        return self.values.get(_to_text(key), default)


class EventTarget:
    def __init__(self, env: "BrowserEnvironment", name: str) -> None:
        self._env = env
        self._name = name

    def addEventListener(self, event_name: Any, callback: Any) -> None:
        self._env.add_event_listener(self._name, event_name, callback)

    def removeEventListener(self, event_name: Any, callback: Any) -> None:
        self._env.remove_event_listener(self._name, event_name, callback)


class DocumentElement:
    def __init__(self, data_build: str = "") -> None:
        self._data_build = str(data_build or "")

    def getAttribute(self, name: Any) -> str | None:
        if str(name or "") == "data-build":
            return self._data_build or None
        return None


@dataclass
class BrowserEnvironment:
    user_agent: str
    script_sources: list[str]
    data_build: str = ""
    page_url: str = "https://chatgpt.com/"
    page_search: str = ""
    language: str = "en-US"
    languages: list[str] = field(default_factory=lambda: ["en-US", "en"])
    hardware_concurrency: int = 8
    screen_width: int = 1920
    screen_height: int = 1080
    local_storage: dict[str, Any] = field(default_factory=dict)

    def __post_init__(self) -> None:
        self._started = time.perf_counter()
        self._time_origin = time.time() * 1000 - self.performance_now()
        self._listeners: dict[str, dict[str, list[Callable[..., Any]]]] = {
            "window": {},
            "document": {},
            "body": {},
        }
        self._scroll_x = 0
        self._scroll_y = 0
        self.window: dict[str, Any] = {}
        self.document: dict[str, Any] = {}
        self.body: dict[str, Any] = {}
        self._build_objects()

    def _build_objects(self) -> None:
        location = {"href": self.page_url, "search": self.page_search}
        history = {"length": 1}
        storage = dict(self.local_storage)
        screen = {"width": self.screen_width, "height": self.screen_height}
        navigator = {
            "userAgent": self.user_agent,
            "language": self.language,
            "languages": list(self.languages),
            "hardwareConcurrency": self.hardware_concurrency,
            "vendor": "Google Inc.",
            "mimeTypes": "[object MimeTypeArray]",
        }
        performance = {
            "now": self.performance_now,
            "timeOrigin": self._time_origin,
            "memory": {
                "jsHeapSizeLimit": 4294705152,
            },
            "cache": {},
        }
        object_api = {
            "create": self.object_create,
            "keys": self.object_keys,
        }
        reflect_api = {
            "set": self.reflect_set,
        }
        math_api = {
            "random": random.random,
            "abs": abs,
            "sqrt": math.sqrt,
        }
        date_api = {
            "now": lambda: int(time.time() * 1000),
        }
        self.body = {
            "addEventListener": EventTarget(self, "body").addEventListener,
            "removeEventListener": EventTarget(self, "body").removeEventListener,
        }
        self.document = {
            "location": location,
            "body": self.body,
            "documentElement": DocumentElement(self.data_build),
            "scripts": [{"src": src} for src in self.script_sources],
            "addEventListener": EventTarget(self, "document").addEventListener,
            "removeEventListener": EventTarget(self, "document").removeEventListener,
        }
        self.window = {
            "window": None,
            "self": None,
            "document": self.document,
            "location": location,
            "history": history,
            "navigator": navigator,
            "screen": screen,
            "localStorage": storage,
            "performance": performance,
            "Math": math_api,
            "Object": object_api,
            "Reflect": reflect_api,
            "Date": date_api,
            "addEventListener": EventTarget(self, "window").addEventListener,
            "removeEventListener": EventTarget(self, "window").removeEventListener,
            "requestIdleCallback": self.request_idle_callback,
            "documentPictureInPicture": None,
            "scrollX": self._scroll_x,
            "scrollY": self._scroll_y,
        }
        self.window["window"] = self.window
        self.window["self"] = self.window

    def performance_now(self) -> float:
        return (time.perf_counter() - self._started) * 1000

    def request_idle_callback(self, callback: Callable[[Any], Any], _options: dict[str, Any] | None = None) -> None:
        callback({"timeRemaining": lambda: 1, "didTimeout": False})

    def object_create(self, *_args: Any) -> OrderedMap:
        return OrderedMap()

    def object_keys(self, value: Any) -> list[str]:
        if isinstance(value, OrderedMap):
            return list(value.keys)
        if isinstance(value, dict):
            return list(value.keys())
        try:
            return list(vars(value).keys())
        except Exception:
            return []

    def reflect_set(self, target: Any, key: Any, value: Any) -> bool:
        if isinstance(target, OrderedMap):
            target.add(key, value)
            return True
        if isinstance(target, dict):
            target[_to_text(key)] = value
            return True
        try:
            setattr(target, _to_text(key), value)
            return True
        except Exception:
            return False

    def add_event_listener(self, target_name: str, event_name: Any, callback: Any) -> None:
        event_key = _to_text(event_name)
        if not callable(callback):
            return
        self._listeners.setdefault(target_name, {}).setdefault(event_key, []).append(callback)

    def remove_event_listener(self, target_name: str, event_name: Any, callback: Any) -> None:
        event_key = _to_text(event_name)
        callbacks = self._listeners.get(target_name, {}).get(event_key)
        if not callbacks:
            return
        self._listeners[target_name][event_key] = [item for item in callbacks if item is not callback]

    def dispatch_event(self, target_name: str, event_name: str, payload: dict[str, Any]) -> None:
        callbacks = list(self._listeners.get(target_name, {}).get(event_name, []))
        for callback in callbacks:
            try:
                callback(payload)
            except Exception:
                continue

    def simulate_default_activity(self, duration_ms: int = 5000) -> None:
        steps = max(3, min(8, int(duration_ms / 800) if duration_ms > 0 else 3))
        base_time = int(time.time() * 1000)
        for index in range(steps):
            timestamp = base_time + index * max(1, duration_ms // max(1, steps))
            self._scroll_x = index * 3
            self._scroll_y = index * 11
            self.window["scrollX"] = self._scroll_x
            self.window["scrollY"] = self._scroll_y
            common = {
                "isTrusted": True,
                "timeStamp": timestamp,
                "target": self.body,
                "currentTarget": self.body,
            }
            self.dispatch_event("window", "pointermove", {**common, "clientX": 90 + index * 5, "clientY": 60 + index * 4})
            self.dispatch_event("window", "wheel", {**common, "deltaX": 0, "deltaY": 10 + index})
            self.dispatch_event("window", "scroll", {**common, "scrollX": self._scroll_x, "scrollY": self._scroll_y})
            self.dispatch_event("window", "keydown", {**common, "key": "a", "code": "KeyA"})
            self.dispatch_event("window", "paste", {**common, "clipboardData": {"types": ["text/plain"]}})
            self.dispatch_event("document", "click", {**common, "clientX": 120 + index, "clientY": 80 + index})
            self.dispatch_event("body", "pointermove", {**common, "clientX": 140 + index, "clientY": 90 + index})


class SentinelDxVm:
    def __init__(self, env: BrowserEnvironment) -> None:
        self.env = env
        self.store: dict[Any, Any] = {}
        self.queue: list[list[Any]] = []
        self.instruction_count = 0
        self.result: str | None = None
        self.error: str | None = None
        self.vm_key = ""

    def reset(self, vm_key: str) -> None:
        self.store.clear()
        self.queue = []
        self.instruction_count = 0
        self.result = None
        self.error = None
        self.vm_key = str(vm_key or "")
        self._install_base_ops()
        self.store[16] = self.vm_key

    def _install_base_ops(self) -> None:
        self.store[0] = self._run_nested
        self.store[1] = self._op_xor
        self.store[2] = self._op_set
        self.store[5] = self._op_concat
        self.store[6] = self._op_get_index
        self.store[7] = self._op_call
        self.store[8] = self._op_copy
        self.store[10] = self.env.window
        self.store[11] = self._op_find_script_match
        self.store[12] = lambda destination: self._set(destination, self.store)
        self.store[13] = self._op_call_raw
        self.store[14] = self._op_json_parse
        self.store[15] = self._op_json_stringify
        self.store[17] = self._op_call_store
        self.store[18] = self._op_atob
        self.store[19] = self._op_btoa
        self.store[20] = self._op_if_equal
        self.store[21] = self._op_if_abs_diff
        self.store[22] = self._op_run_queue
        self.store[23] = self._op_if_defined
        self.store[24] = self._op_bind
        self.store[25] = lambda *_args: None
        self.store[26] = lambda *_args: None
        self.store[27] = self._op_remove_or_subtract
        self.store[28] = lambda *_args: None
        self.store[29] = self._op_less_than
        self.store[30] = self._op_make_closure
        self.store[33] = self._op_multiply
        self.store[34] = self._op_resolve_like
        self.store[35] = self._op_divide

    def execute(
        self,
        dx: str,
        *,
        vm_key: str | None = None,
        reset: bool = False,
        fallback_result: Callable[[int], str | None] | None = None,
        encode_success: bool = True,
    ) -> str | None:
        if reset or 16 not in self.store:
            self.reset(str(vm_key or self.vm_key or ""))
        elif vm_key is not None:
            self.store[16] = str(vm_key or "")
            self.vm_key = str(vm_key or "")
        self._install_result_ops(encode_success=encode_success)
        try:
            decoded = _b64_decode_text(dx)
            queue = json.loads(xor_string(decoded, _to_text(self._get(16))))
            if not isinstance(queue, list):
                raise ValueError("invalid dx payload")
            self._set(9, queue)
            self._drain_queue()
        except Exception as error:
            if fallback_result is not None:
                return fallback_result(self.instruction_count)
            return _b64_encode_text(f"{self.instruction_count}: {error}")
        if self.error is not None:
            if fallback_result is not None:
                return fallback_result(self.instruction_count)
            return self.error
        if self.result is not None:
            return self.result
        if fallback_result is not None:
            return fallback_result(self.instruction_count)
        return None

    def _install_result_ops(self, *, encode_success: bool) -> None:
        self.result = None
        self.error = None

        def set_success(value: Any) -> None:
            text = _to_text(value)
            self.result = _b64_encode_text(text) if encode_success else text

        def set_error(value: Any) -> None:
            self.error = _b64_encode_text(_to_text(value))

        self.store[3] = set_success
        self.store[4] = set_error

    def _get(self, key: Any) -> Any:
        return self.store.get(key)

    def _set(self, key: Any, value: Any) -> None:
        self.store[key] = value

    def _drain_queue(self) -> None:
        while self.queue_length() > 0:
            queue = self._get(9)
            if not isinstance(queue, list):
                break
            item = queue.pop(0)
            if not isinstance(item, list) or not item:
                self.instruction_count += 1
                continue
            opcode, *args = item
            handler = self._get(opcode)
            if callable(handler):
                handler(*args)
            self.instruction_count += 1

    def queue_length(self) -> int:
        queue = self._get(9)
        return len(queue) if isinstance(queue, list) else 0

    def _resolve(self, ref: Any) -> Any:
        return self._get(ref)

    def _op_xor(self, destination: Any, key_ref: Any) -> None:
        self._set(destination, xor_string(_to_text(self._get(destination)), _to_text(self._get(key_ref))))

    def _op_set(self, destination: Any, value: Any) -> None:
        self._set(destination, value)

    def _op_concat(self, destination: Any, source: Any) -> None:
        current = self._get(destination)
        incoming = self._get(source)
        if isinstance(current, list):
            current.append(incoming)
            return
        self._set(destination, _to_text(current) + _to_text(incoming))

    def _op_remove_or_subtract(self, destination: Any, source: Any) -> None:
        current = self._get(destination)
        incoming = self._get(source)
        if isinstance(current, list):
            try:
                current.remove(incoming)
            except ValueError:
                pass
            return
        try:
            self._set(destination, current - incoming)
        except Exception:
            self._set(destination, 0)

    def _op_less_than(self, destination: Any, left: Any, right: Any) -> None:
        self._set(destination, self._get(left) < self._get(right))

    def _op_multiply(self, destination: Any, left: Any, right: Any) -> None:
        self._set(destination, float(self._get(left) or 0) * float(self._get(right) or 0))

    def _op_divide(self, destination: Any, left: Any, right: Any) -> None:
        denominator = float(self._get(right) or 0)
        self._set(destination, 0 if denominator == 0 else float(self._get(left) or 0) / denominator)

    def _op_get_index(self, destination: Any, target_ref: Any, key_ref: Any) -> None:
        target = self._get(target_ref)
        key = self._get(key_ref)
        self._set(destination, self._read_property(target, key))

    def _op_call(self, target_ref: Any, *arg_refs: Any) -> None:
        target = self._get(target_ref)
        if not callable(target):
            return
        target(*[self._get(arg_ref) for arg_ref in arg_refs])

    def _op_call_store(self, destination: Any, target_ref: Any, *arg_refs: Any) -> None:
        target = self._get(target_ref)
        if not callable(target):
            return
        try:
            self._set(destination, target(*[self._get(arg_ref) for arg_ref in arg_refs]))
        except Exception as error:
            self._set(destination, _to_text(error))

    def _op_call_raw(self, destination: Any, target_ref: Any, *arg_refs: Any) -> None:
        target = self._get(target_ref)
        if not callable(target):
            return
        try:
            target(*arg_refs)
        except Exception as error:
            self._set(destination, _to_text(error))

    def _op_copy(self, destination: Any, source: Any) -> None:
        self._set(destination, self._get(source))

    def _op_find_script_match(self, destination: Any, pattern_ref: Any) -> None:
        pattern = _to_text(self._get(pattern_ref))
        compiled = re.compile(pattern)
        for script in self.env.document.get("scripts", []):
            src = _to_text((script or {}).get("src"))
            matched = compiled.search(src)
            if matched:
                self._set(destination, matched.group(0))
                return
        self._set(destination, None)

    def _op_json_parse(self, destination: Any, source: Any) -> None:
        self._set(destination, json.loads(_to_text(self._get(source))))

    def _op_json_stringify(self, destination: Any, source: Any) -> None:
        self._set(destination, json.dumps(self._get(source), separators=(",", ":"), ensure_ascii=False))

    def _op_atob(self, destination: Any) -> None:
        self._set(destination, _b64_decode_text(self._get(destination)))

    def _op_btoa(self, destination: Any) -> None:
        self._set(destination, _b64_encode_text(self._get(destination)))

    def _op_if_equal(self, left_ref: Any, right_ref: Any, target_ref: Any, *arg_refs: Any) -> None:
        if self._get(left_ref) != self._get(right_ref):
            return
        target = self._get(target_ref)
        if callable(target):
            target(*arg_refs)

    def _op_if_abs_diff(self, left_ref: Any, right_ref: Any, threshold_ref: Any, target_ref: Any, *arg_refs: Any) -> None:
        try:
            delta = abs(float(self._get(left_ref) or 0) - float(self._get(right_ref) or 0))
            threshold = float(self._get(threshold_ref) or 0)
        except Exception:
            return
        if delta <= threshold:
            return
        target = self._get(target_ref)
        if callable(target):
            target(*arg_refs)

    def _op_if_defined(self, value_ref: Any, target_ref: Any, *arg_refs: Any) -> None:
        if self._get(value_ref) is None:
            return
        target = self._get(target_ref)
        if callable(target):
            target(*arg_refs)

    def _op_bind(self, destination: Any, target_ref: Any, key_ref: Any) -> None:
        target = self._get(target_ref)
        prop = self._get(key_ref)
        value = self._read_property(target, prop)
        self._set(destination, value)

    def _op_resolve_like(self, destination: Any, source_ref: Any) -> None:
        self._set(destination, self._get(source_ref))

    def _op_make_closure(self, destination: Any, result_ref: Any, capture_refs: Any, nested_queue: Any) -> None:
        capture_list = list(capture_refs) if isinstance(capture_refs, list) and isinstance(nested_queue, list) else []
        if isinstance(nested_queue, list):
            nested = list(nested_queue)
        elif isinstance(capture_refs, list):
            nested = list(capture_refs)
            capture_list = []
        else:
            nested = []

        def closure(*call_args: Any) -> Any:
            saved_queue = list(self._get(9) or [])
            if capture_list:
                for index, ref in enumerate(capture_list):
                    if index < len(call_args):
                        self._set(ref, call_args[index])
            self._set(9, list(nested))
            try:
                self._drain_queue()
                return self._get(result_ref)
            finally:
                self._set(9, saved_queue)

        self._set(destination, closure)

    def _run_nested(self, dx_ref: Any) -> None:
        payload = self._get(dx_ref)
        if not isinstance(payload, str):
            return
        saved_queue = list(self._get(9) or [])
        try:
            decoded = _b64_decode_text(payload)
            nested_queue = json.loads(xor_string(decoded, _to_text(self._get(16))))
            self._set(9, nested_queue)
            self._drain_queue()
        finally:
            self._set(9, saved_queue)

    def _op_run_queue(self, destination: Any, queue_value: Any) -> None:
        nested_queue = list(queue_value) if isinstance(queue_value, list) else []
        saved_queue = list(self._get(9) or [])
        try:
            self._set(9, nested_queue)
            self._drain_queue()
        except Exception as error:
            self._set(destination, _to_text(error))
        finally:
            self._set(9, saved_queue)

    def _read_property(self, target: Any, key: Any) -> Any:
        if target is None:
            return None
        if isinstance(target, OrderedMap):
            if isinstance(key, str) and hasattr(target, key):
                return getattr(target, key)
            return target.get(key)
        if isinstance(target, dict):
            return target.get(_to_text(key)) if _to_text(key) in target else target.get(key)
        if isinstance(target, (list, tuple)):
            try:
                return target[int(key)]
            except Exception:
                return None
        if isinstance(target, str):
            try:
                return target[int(key)]
            except Exception:
                return getattr(target, _to_text(key), None)
        try:
            return getattr(target, _to_text(key))
        except Exception:
            pass
        try:
            return target[key]
        except Exception:
            return None


def build_browser_environment(
    *,
    user_agent: str,
    script_sources: list[str],
    data_build: str = "",
    page_url: str = "https://chatgpt.com/",
) -> BrowserEnvironment:
    return BrowserEnvironment(
        user_agent=user_agent,
        script_sources=script_sources,
        data_build=data_build,
        page_url=page_url,
        page_search="",
    )


def solve_turnstile_dx(
    dx: str,
    vm_key: str,
    env: BrowserEnvironment,
) -> str | None:
    runtime = SentinelDxVm(env)
    return runtime.execute(
        dx,
        vm_key=vm_key,
        reset=True,
        fallback_result=lambda count: str(count),
        encode_success=True,
    )


class SessionObserverRuntime:
    def __init__(self, env: BrowserEnvironment, vm_key: str) -> None:
        self.env = env
        self.runtime = SentinelDxVm(env)
        self.runtime.reset(vm_key)

    def run_collector(self, dx: str) -> str | None:
        return self.runtime.execute(dx, reset=False, encode_success=True)

    def simulate_default_activity(self, duration_ms: int = 5000) -> None:
        self.env.simulate_default_activity(duration_ms)

    def run_snapshot(self, dx: str) -> str | None:
        return self.runtime.execute(dx, reset=False, encode_success=True)
