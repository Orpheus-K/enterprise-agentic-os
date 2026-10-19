"""Inspect the compute package without pretending a Worker service is running."""

import argparse
from importlib.metadata import version


def main() -> None:
    parser = argparse.ArgumentParser(
        description="AgentOS compute package; RPC runtime not implemented"
    )
    parser.add_argument("--version", action="version", version=version("agentos-compute"))
    parser.parse_args()
    parser.print_help()


if __name__ == "__main__":
    main()
