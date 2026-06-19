# mitmproxy script for the strip-STARTTLS scenario (Finding #184).
#
# This is the one item flagged in docs/e2e-coverage-strategy.md §5 #1
# as a real-software fixture with a test-driven configuration. The
# script runs INSIDE mitmproxy (real software, widely-deployed). The
# script body itself is a test artifact because no real-world relay
# advertises STARTTLS and then fails the upgrade — the misbehavior is
# the point of the test.
#
# When loaded by `mitmdump -s strip_starttls.py`, this script
# intercepts the upstream SMTP EHLO response, strips the
# 250-STARTTLS extension, and forwards traffic plaintext.

from mitmproxy import tcp


def tcp_message(flow: tcp.TCPFlow) -> None:
    """Strip STARTTLS from the upstream EHLO advertisement."""
    msg = flow.messages[-1]
    if msg.from_client:
        return
    text = msg.content.decode(errors="replace")
    if "250-STARTTLS" not in text:
        return
    cleaned_lines = [line for line in text.splitlines(keepends=True) if "STARTTLS" not in line]
    msg.content = "".join(cleaned_lines).encode()
