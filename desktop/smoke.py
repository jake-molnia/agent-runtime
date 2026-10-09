#!/usr/bin/env python3
"""Exercise a real sandbox desktop through MCP, using only Python's stdlib.

Run inside the sandbox's process and network namespaces. Attach to an existing
runtime, or use --start /path/to/agent-runtime to own its startup and cleanup.
"""

import argparse
import base64
import contextlib
import functools
import http.server
import json
import os
from pathlib import Path
import shlex
import signal
import struct
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request
import uuid


HEALTH = "http://127.0.0.1:8081/health"
AIO = "http://127.0.0.1:18091"
BROWSER = "http://127.0.0.1:8931/mcp"
SENTINEL_KEY = "OPENAI_API_KEY"


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def wait_for(description, check, timeout=15):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        try:
            result = check()
            if result:
                return result
        except (OSError, ValueError) as error:
            last = error
        time.sleep(0.2)
    raise RuntimeError(f"timed out waiting for {description}: {last or 'condition false'}")


def http_get(url):
    with urllib.request.urlopen(url, timeout=5) as response:
        return response.read()


class MCP:
    def __init__(self, url):
        self.url = url
        self.sequence = 0
        self.headers = {
            "Content-Type": "application/json",
            "Accept": "application/json, text/event-stream",
            "MCP-Protocol-Version": "2024-11-05",
        }
        result = self.request("initialize", {
            "protocolVersion": "2024-11-05", "capabilities": {},
            "clientInfo": {"name": "desktop-smoke", "version": "1"},
        })
        self.headers["MCP-Protocol-Version"] = result["protocolVersion"]
        self.request("notifications/initialized", {}, notification=True)
        self.tools = {}
        params = {}
        while True:
            result = self.request("tools/list", params)
            self.tools.update((tool["name"], tool) for tool in result["tools"])
            if not result.get("nextCursor"):
                break
            params = {"cursor": result["nextCursor"]}

    def request(self, method, params, notification=False):
        self.sequence += 1
        message = {"jsonrpc": "2.0", "method": method, "params": params}
        if not notification:
            message["id"] = self.sequence
        request = urllib.request.Request(
            self.url, json.dumps(message).encode(), self.headers,
        )
        with urllib.request.urlopen(request, timeout=45) as response:
            if response.headers.get("Mcp-Session-Id"):
                self.headers["Mcp-Session-Id"] = response.headers["Mcp-Session-Id"]
            if notification:
                return None
            if response.headers.get("Content-Type", "").startswith("text/event-stream"):
                data = []
                reply = None
                for line in response:
                    line = line.decode().rstrip("\r\n")
                    if line.startswith("data:"):
                        data.append(line[5:].lstrip())
                    elif not line and data:
                        candidate = json.loads("\n".join(data))
                        data = []
                        if candidate.get("id") == self.sequence:
                            reply = candidate
                            break
                require(reply is not None, f"{method}: SSE ended without a response")
            else:
                reply = json.load(response)
        require("error" not in reply, f"{method}: {reply.get('error')}")
        return reply["result"]

    def call(self, tool, **arguments):
        require(tool in self.tools, f"MCP tool missing: {tool}")
        result = self.request("tools/call", {"name": tool, "arguments": arguments})
        require(not result.get("isError"), f"{tool}: {tool_text(result)[:1500]}")
        return result

    def close(self):
        if "Mcp-Session-Id" in self.headers:
            request = urllib.request.Request(self.url, headers=self.headers, method="DELETE")
            with contextlib.suppress(OSError):
                urllib.request.urlopen(request, timeout=2).close()


def tool_text(result):
    return "\n".join(item.get("text", "") for item in result.get("content", []))


def expect_text(result, expected):
    text = tool_text(result)
    require(expected in text, f"expected {expected!r} in tool result: {text[:1500]}")


def screenshot(aio, destination):
    result = aio.call("browser_gui_screenshot")
    images = [item for item in result.get("content", []) if item.get("type") == "image"]
    require(images, "desktop screenshot did not return MCP image content")
    require(images[0].get("mimeType") == "image/png", "desktop screenshot is not PNG")
    data = base64.b64decode(images[0]["data"], validate=True)
    require(data[:8] == b"\x89PNG\r\n\x1a\n", "invalid PNG screenshot")
    size = struct.unpack(">II", data[16:24])
    require(size == (1440, 900), f"expected full 1440x900 desktop, got {size}")
    destination.write_bytes(data)


def process_table():
    result = {}
    for directory in Path("/proc").iterdir():
        if not directory.name.isdigit():
            continue
        try:
            stat = (directory / "stat").read_text().rsplit(") ", 1)[1].split()
            result[int(directory.name)] = {
                "ppid": int(stat[1]), "start": stat[19], "state": stat[0],
                "command": (directory / "cmdline").read_bytes().replace(b"\0", b" ").decode(errors="replace"),
            }
        except (OSError, IndexError, ValueError):
            continue
    return result


def descendants(pid):
    table = process_table()
    selected = {pid}
    while True:
        children = {key for key, value in table.items() if value["ppid"] in selected}
        if children <= selected:
            return {key: value for key, value in table.items() if key in selected and key != pid}
        selected |= children


def remaining_processes(recorded):
    table = process_table()
    return [pid for pid, process in recorded.items()
            if pid in table and table[pid]["start"] == process["start"]]


def check_environments(pid, sentinel):
    checked = 0
    for child in descendants(pid):
        try:
            raw = Path(f"/proc/{child}/environ").read_bytes()
        except FileNotFoundError:
            continue
        environment = dict(entry.split(b"=", 1) for entry in raw.split(b"\0") if b"=" in entry)
        if b"agent-desktop-" not in environment.get(b"HOME", b""):
            continue
        checked += 1
        for key in [SENTINEL_KEY, "ANTHROPIC_API_KEY", "OPENCODE_SERVER_PASSWORD", "TAILSCALE_AUTHKEY"]:
            require(key.encode() not in environment, f"credential variable {key} inherited by desktop PID {child}")
        if sentinel:
            require(sentinel.encode() not in raw, f"sentinel inherited by desktop PID {child}")
    require(checked >= 8, f"only found {checked} desktop processes to inspect")
    return checked


class FixtureHandler(http.server.SimpleHTTPRequestHandler):
    def log_message(self, *_args):
        pass


def exercise(aio, browser, output, workspace):
    expected = {
        "sandbox_execute_bash", "sandbox_execute_code", "sandbox_file_operations",
        "sandbox_str_replace_editor", "sandbox_get_context", "sandbox_get_packages",
        "sandbox_load_skill", "browser_get_info", "browser_gui_screenshot",
        "browser_gui_execute_action", "documents_convert_to_markdown",
    }
    require(expected <= aio.tools.keys(), f"AIO tools missing: {sorted(expected - aio.tools.keys())}")
    (output / "tools.json").write_text(json.dumps({
        "aio": sorted(aio.tools), "browser": sorted(browser.tools),
    }, indent=2) + "\n")
    token = uuid.uuid4().hex
    with tempfile.TemporaryDirectory(prefix="desktop-smoke-", dir=workspace) as temporary:
        fixture = Path(temporary)
        html = f"""<!doctype html><html><head><title>DesktopSmoke-{token}</title>
<style>body{{background:#103e72;color:white;font:28px sans-serif;padding:50px}}
input{{font:28px sans-serif;width:600px;padding:15px}}</style></head>
<body><h1>Shared desktop browser</h1><label for="input">Desktop input</label>
<p><input id="input" value="initial"></p><p>Document proof {token}</p></body></html>"""
        aio.call("sandbox_file_operations", action="write", path=str(fixture / "index.html"), content=html)
        expect_text(aio.call("sandbox_file_operations", action="read", path=str(fixture / "index.html")), token)
        expect_text(aio.call("sandbox_execute_bash", cmd="printf shell-%s " + shlex.quote(token), cwd=str(fixture)), "shell-" + token)
        for language, code in [("python", f"print('python-' + '{token}')"),
                               ("javascript", f"console.log('javascript-' + '{token}')")]:
            expect_text(aio.call("sandbox_execute_code", language=language, code=code), language + "-" + token)
        env_code = "import os; assert not any(k in os.environ for k in " + repr([
            SENTINEL_KEY, "ANTHROPIC_API_KEY", "OPENCODE_SERVER_PASSWORD", "TAILSCALE_AUTHKEY",
        ]) + "); print('desktop-environment-clean')"
        expect_text(aio.call("sandbox_execute_bash", cmd="python3 -c " + shlex.quote(env_code), cwd=str(fixture)), "desktop-environment-clean")

        handler = functools.partial(FixtureHandler, directory=str(fixture))
        with http.server.ThreadingHTTPServer(("127.0.0.1", 18782), handler) as server:
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            try:
                browser.call("browser_navigate", url="http://127.0.0.1:18782/index.html")
                video = (fixture / "browser-proof.webm").resolve()
                browser.call("browser_start_video", filename=str(video), size={"width": 640, "height": 400}, fps=10)
                try:
                    browser.call("browser_run_code_unsafe", code="async (page) => { await page.bringToFront(); await page.locator('#input').fill('playwright-edit'); await page.locator('#input').focus(); }")
                    expect_text(browser.call("browser_evaluate", function="() => document.querySelector('#input').value"), "playwright-edit")
                    screenshot(aio, output / "browser-before.png")
                    aio.call("browser_gui_execute_action", action={"action_type": "HOTKEY", "keys": ["ctrl", "a"]})
                    aio.call("browser_gui_execute_action", action={"action_type": "TYPING", "text": "desktop-edit-" + token})
                    expect_text(browser.call("browser_evaluate", function="() => document.querySelector('#input').value"), "desktop-edit-" + token)
                    screenshot(aio, output / "browser-after.png")
                finally:
                    browser.call("browser_stop_video")
                require(video.is_file() and video.stat().st_size > 0, "browser video artifact is empty or absent")
                video_data = video.read_bytes()
                require(video_data.startswith(b"\x1a\x45\xdf\xa3"), "browser video is not a WebM container")
                (output / "browser-proof.webm").write_bytes(video_data)
                conversion = aio.call("documents_convert_to_markdown", uri=(fixture / "index.html").as_uri())
                expect_text(conversion, "Document proof " + token)
                (output / "converted-document.md").write_text(tool_text(conversion))

                title = "DesktopTerminal-" + token
                command = "xfce4-terminal --disable-server --title=" + shlex.quote(title) + " --command='bash --noprofile --norc' >/dev/null 2>&1 &"
                aio.call("sandbox_execute_bash", cmd=command, cwd=str(fixture))

                def terminal_window():
                    payload = json.loads(http_get(AIO + "/v2/computer/windows"))
                    return next((window for window in payload.get("data", payload)["windows"] if title in window["title"]), None)

                window = wait_for("terminal window", terminal_window)
                aio.call("browser_gui_execute_action", action={"action_type": "WINDOW_ACTIVATE", "window_id": window["window_id"]})
                aio.call("browser_gui_execute_action", action={"action_type": "WAIT", "duration": 0.5})
                terminal_file = fixture / "terminal-proof.txt"
                typed_command = "printf '%s\\n' " + shlex.quote(token) + " > " + shlex.quote(str(terminal_file))
                aio.call("browser_gui_execute_action", action={"action_type": "TYPING", "text": typed_command})
                aio.call("browser_gui_execute_action", action={"action_type": "PRESS", "key": "enter"})
                wait_for("terminal output file", lambda: terminal_file.exists() and terminal_file.read_text().strip() == token)
                expect_text(aio.call("sandbox_file_operations", action="read", path=str(terminal_file)), token)
                screenshot(aio, output / "terminal.png")
                aio.call("browser_gui_execute_action", action={"action_type": "TYPING", "text": "exit"})
                aio.call("browser_gui_execute_action", action={"action_type": "PRESS", "key": "enter"})
            finally:
                server.shutdown()
                thread.join(timeout=5)
    require(b"noVNC" in http_get("http://127.0.0.1:6080/vnc.html"), "noVNC page unavailable")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--start", metavar="BINARY", help="start BINARY serve and stop it after the test")
    parser.add_argument("--pid", type=int, help="PID of an existing runtime for process and environment checks")
    parser.add_argument("--check-failure", action="store_true", help="kill the computer worker and verify runtime shutdown; requires --start or --pid")
    parser.add_argument("--workspace", default=os.environ.get("SANDBOX_ROOT", "/workspace"))
    parser.add_argument("--output-dir", default="/tmp/desktop-evidence")
    args = parser.parse_args()
    if args.start and args.pid:
        parser.error("use either --start or --pid")
    if args.check_failure and not (args.start or args.pid):
        parser.error("--check-failure requires --start or --pid")
    output = Path(args.output_dir).resolve()
    output.mkdir(parents=True, exist_ok=True)
    process = None
    clients = []
    sentinel = None
    log = None
    recorded = {}
    try:
        if args.start:
            sentinel = "desktop-smoke-secret-" + uuid.uuid4().hex
            environment = os.environ.copy()
            environment[SENTINEL_KEY] = sentinel
            environment["SANDBOX_ROOT"] = args.workspace
            log = (output / "runtime.log").open("wb")
            process = subprocess.Popen([args.start, "serve"], env=environment, stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
            args.pid = process.pid

        def ready():
            if process and process.poll() is not None:
                raise RuntimeError(f"runtime exited with {process.returncode}; inspect {output / 'runtime.log'}")
            return http_get(HEALTH) is not None

        wait_for("runtime /health", ready, timeout=120)
        print("PASS runtime readiness", flush=True)
        aio = MCP(AIO + "/mcp")
        clients.append(aio)
        browser = MCP(BROWSER)
        clients.append(browser)
        exercise(aio, browser, output, args.workspace)
        print("PASS shell, files, Python, JavaScript, document conversion, shared browser/desktop input, terminal input, PNG screenshots, browser video, noVNC", flush=True)
        if args.pid:
            checked = check_environments(args.pid, sentinel)
            print(f"PASS credential environment checks across {checked} desktop processes", flush=True)
            recorded = descendants(args.pid)
        else:
            print("SKIP /proc credential and shutdown checks: provide --start or --pid", flush=True)
        if args.check_failure:
            workers = [pid for pid, entry in recorded.items()
                       if entry["command"].split() and Path(entry["command"].split()[0]).name == "computer-use"]
            require(len(workers) == 1, f"expected one computer worker, found {len(workers)}")
            os.kill(workers[0], signal.SIGKILL)
            wait_for("runtime exit after worker failure", lambda: process.poll() is not None if process else args.pid not in process_table(), timeout=20)
            wait_for("all descendants reaped after failure", lambda: not remaining_processes(recorded), timeout=15)
            print("PASS worker failure stops runtime and reaps descendants", flush=True)
    finally:
        for client in clients:
            client.close()
        if process and process.poll() is None:
            recorded.update(descendants(process.pid))
            process.terminate()
            try:
                process.wait(timeout=15)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait(timeout=5)
            wait_for("descendants reaped after cancellation", lambda: not remaining_processes(recorded), timeout=15)
        if log:
            log.close()
    (output / "result.json").write_text(json.dumps({"passed": True, "failure_checked": args.check_failure, "processes_checked": bool(args.pid)}, indent=2) + "\n")
    print(f"Evidence: {output}", flush=True)


if __name__ == "__main__":
    try:
        main()
    except (OSError, RuntimeError, ValueError, KeyError) as error:
        print(f"FAIL: {error}", file=sys.stderr)
        sys.exit(1)
