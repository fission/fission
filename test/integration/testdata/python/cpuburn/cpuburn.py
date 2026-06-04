# SPDX-FileCopyrightText: The Fission Authors
#
# SPDX-License-Identifier: Apache-2.0

import time


def main():
    # Burn ~50ms of CPU per request so HPA tests consume a deterministic
    # amount of cpu regardless of request rate or handler efficiency.
    # time.process_time() counts CPU time (not wall clock), so the burn is
    # stable under CI node contention.
    start = time.process_time()
    x = 0
    while time.process_time() - start < 0.05:
        x += 1
    return "burned\n"
