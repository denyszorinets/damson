# Damson callback plugin: writes one JSON line per event to the file named by
# DAMSON_EVENTS_FILE. It records names and counts only, never task results,
# which can hold secrets. The Go side reads the final "stats" event.
from __future__ import absolute_import, division, print_function

__metaclass__ = type

DOCUMENTATION = """
    name: damson_events
    type: notification
    short_description: Writes Damson step events as JSON lines
    description:
      - Writes play, task, host result and recap events to DAMSON_EVENTS_FILE.
    requirements:
      - enable in ANSIBLE_CALLBACKS_ENABLED
"""

import json
import os

from ansible.plugins.callback import CallbackBase


class CallbackModule(CallbackBase):
    CALLBACK_VERSION = 2.0
    CALLBACK_TYPE = "notification"
    CALLBACK_NAME = "damson_events"
    CALLBACK_NEEDS_ENABLED = True

    def __init__(self):
        super(CallbackModule, self).__init__()
        path = os.environ.get("DAMSON_EVENTS_FILE")
        self._out = open(path, "a", buffering=1) if path else None
        self._task = ""

    def _emit(self, event):
        if self._out is not None:
            self._out.write(json.dumps(event, sort_keys=True) + "\n")

    def v2_playbook_on_play_start(self, play):
        self._emit({"event": "play", "name": play.get_name().strip()})

    def v2_playbook_on_task_start(self, task, is_conditional):
        self._task = task.get_name().strip()
        self._emit({"event": "task", "name": self._task})

    def _host(self, result, status):
        self._emit({"event": "result", "host": result._host.get_name(), "task": self._task, "status": status})

    def v2_runner_on_ok(self, result):
        self._host(result, "changed" if result._result.get("changed", False) else "ok")

    def v2_runner_on_failed(self, result, ignore_errors=False):
        self._host(result, "ignored" if ignore_errors else "failed")

    def v2_runner_on_unreachable(self, result):
        self._host(result, "unreachable")

    def v2_runner_on_skipped(self, result):
        self._host(result, "skipped")

    def v2_playbook_on_stats(self, stats):
        hosts = {}
        for h in sorted(stats.processed.keys()):
            s = stats.summarize(h)
            hosts[h] = {
                "ok": s.get("ok", 0),
                "changed": s.get("changed", 0),
                "failed": s.get("failures", 0),
                "unreachable": s.get("unreachable", 0),
                "skipped": s.get("skipped", 0),
            }
        self._emit({"event": "stats", "hosts": hosts})
        if self._out is not None:
            self._out.close()
            self._out = None
