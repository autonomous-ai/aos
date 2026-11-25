"""Reachy release() keeps torque until the sleep pose lands; tracking can hand the body back.

Covers hal/drivers/motors/reachy_service.py:
- release() retries goto_sleep while the daemon still reports the cancelled move,
  and never disables motors when the sleep pose was not reached.
- idle_recording exists, so drivers/tracking/body.py can hand the body back.
"""
import os
import sys
import types
import unittest
from unittest import mock

sys.path.append(os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__)))))


def _install_sdk_stub():
    """Register a minimal fake `reachy_mini` package before importing the driver."""
    if "reachy_mini" in sys.modules:
        return
    pkg = types.ModuleType("reachy_mini")
    pkg.ReachyMini = object
    utils = types.ModuleType("reachy_mini.utils")
    utils.create_head_pose = lambda **kwargs: None
    motion = types.ModuleType("reachy_mini.motion")
    recorded = types.ModuleType("reachy_mini.motion.recorded_move")
    recorded.RecordedMoves = object
    sys.modules.update({
        "reachy_mini": pkg,
        "reachy_mini.utils": utils,
        "reachy_mini.motion": motion,
        "reachy_mini.motion.recorded_move": recorded,
    })


_install_sdk_stub()

import hal.presets as P  # noqa: E402
import hal.drivers.motors.reachy_service as rs  # noqa: E402
from hal.drivers.motors.reachy_service import ReachyMotionService  # noqa: E402


class SleepMini:
    """Daemon stand-in that refuses goto_sleep for the first `busy` calls."""

    def __init__(self, busy):
        self.busy = busy
        self.calls = []

    def cancel_move(self):
        self.calls.append("cancel_move")

    def goto_sleep(self):
        self.calls.append("goto_sleep")
        if self.busy > 0:
            self.busy -= 1
            raise RuntimeError("a move is already running")

    def disable_motors(self):
        self.calls.append("disable_motors")


class TestReachyRelease(unittest.TestCase):
    def _svc(self, busy):
        svc = ReachyMotionService()
        svc._mini = SleepMini(busy)
        return svc

    def test_retries_sleep_then_disables_torque(self):
        svc = self._svc(busy=2)
        with mock.patch.object(rs.time, "sleep"):
            errors = svc.release()
        self.assertEqual(errors, {})
        self.assertEqual(svc._mini.calls, ["goto_sleep"] * 3 + ["disable_motors"])

    def test_keeps_torque_when_sleep_never_lands(self):
        svc = self._svc(busy=99)
        with mock.patch.object(rs.time, "sleep"):
            errors = svc.release()
        self.assertIn("sleep", errors)
        self.assertNotIn("disable_motors", svc._mini.calls)
        self.assertEqual(svc._mini.calls.count("goto_sleep"), rs._SLEEP_ATTEMPTS)

    def test_waits_for_cancelled_move_before_sleeping(self):
        svc = self._svc(busy=0)
        thread = mock.Mock()
        thread.is_alive.return_value = True
        svc._play_thread = thread
        svc.release()
        thread.join.assert_called_with(timeout=rs._RELEASE_JOIN_S)
        self.assertEqual(svc._mini.calls[-2:], ["goto_sleep", "disable_motors"])


class TestReachyIdleRecording(unittest.TestCase):
    def test_idle_recording_is_the_idle_preset(self):
        svc = ReachyMotionService()
        self.assertEqual(svc.idle_recording, P.SERVO_IDLE)


if __name__ == "__main__":
    unittest.main()
