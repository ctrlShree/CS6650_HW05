"""Step-load stress test for the Product API.

Adds STEP_USERS users every STEP_SECONDS until MAX_USERS, then stops.
Same request mix as locustfile.py. The point is to find the step where
throughput stops rising and latency starts climbing.

Locust uses the StepLoadShape class automatically, so the user count and
spawn rate typed into the web UI are ignored.
"""

from locust import LoadTestShape

# Importing the user classes makes them available in this file.
from locustfile import ProductFastHttpUser, ProductHttpUser  # noqa: F401

STEP_USERS = 200    # users added at each step
STEP_SECONDS = 45   # how long each step lasts
MAX_USERS = 2000    # stop after this many users
SPAWN_RATE = 50     # users started per second when a step begins


class StepLoadShape(LoadTestShape):
    def tick(self):
        run_time = self.get_run_time()
        users = (int(run_time // STEP_SECONDS) + 1) * STEP_USERS
        if users > MAX_USERS:
            return None  # returning None ends the test
        return users, SPAWN_RATE
