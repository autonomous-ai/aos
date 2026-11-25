"""Which URLs point at the Autonomous cloud API."""

from urllib.parse import urlparse

AUTONOMOUS_DOMAINS = ("autonomous.ai", "autonomousdev.xyz")


def is_autonomous_host(base: str) -> bool:
    """True when base's host is one of AUTONOMOUS_DOMAINS or a subdomain of one."""
    try:
        host = (urlparse(base).hostname or "").lower()
    except ValueError:
        return False
    return any(host == d or host.endswith("." + d) for d in AUTONOMOUS_DOMAINS)
